import { createEffect, createSignal, For, Match, onCleanup, onMount, Show, Switch } from "solid-js";
import type { JSX } from "solid-js";
import { createStore } from "solid-js/store";
import {
  alignStorySentences,
  archiveSession,
  deleteDictionaryEntry,
  getDefinition,
  getDefinitionOptions,
  getStory,
  getStorySentenceAudio,
  getStorySentenceAlignment,
  postReaderEvents,
  putDictionaryEntry,
  setReaderSurfaceKnowledge,
  sentenceBreakdown,
  saveReadingProgress,
  startReading,
  setWordKnowledge,
  generateStoryTasks,
  updateStory,
  wordBreakdown,
} from "../api";
import type { APISchema } from "../api";
import { hapticTick } from "../haptics";
import { routeHref, sessionHref } from "../router";
import { appStore } from "../store";

type StoryToken = APISchema<"StoryToken">;
type StoryLoad = APISchema<"StoryLoad">;
type StoryPageWindow = NonNullable<StoryLoad["window"]>;
type PagedStoryLoad = StoryLoad & { window: StoryPageWindow };
type ReaderKnowledge = APISchema<"ReaderKnowledge">;
type ReaderSurfaceKnowledge = APISchema<"ReaderSurfaceKnowledge">;
type KnowledgeLevel = ReaderKnowledge["level"];
type Definition = APISchema<"Definition">;
type DefinitionOption = APISchema<"DefinitionOption">;
type ReaderEvent = APISchema<"ReaderEvent">;
type SentenceSpan = APISchema<"SentenceSpan">;
type ReaderWordTiming = APISchema<"ReaderWordTiming">;

type Loadable<T> = "loading" | "error" | T;
type RemovalNotice = "removed" | "removed-failed";
type AudioGeneration = {
  status: "idle" | "generating" | "error";
  phase?: "tts" | "alignment";
  completed: number;
  total: number;
};
type TaskSelection = {
  startPosition: number;
  endPosition: number;
  wordCount: number;
};
type Analysis = { mode: "sentence"; position: number } | { mode: "word"; key: string } | null;
type RadialButton = {
  event: string;
  value: KnowledgeLevel;
  label: string;
  hint: string;
  level: string;
  x: number;
  y: number;
  angle: number;
};
type RadialGeom = {
  position: number;
  key: string;
  surface: string;
  level: string;
  pressX: number;
  pressY: number;
  cx: number;
  cy: number;
  wordHalfW: number;
  wordHalfH: number;
  side: "above" | "below";
  buttons: RadialButton[];
  bubbleX: number;
  bubbleY: number;
  maxReach: number;
};
type ReaderDisplaySettings = {
  colorKnowledge: boolean;
  lineHighlight: boolean;
  wordHighlight: boolean;
  popupEnabled: boolean;
  flowMode: "pages" | "infinite";
  swipeAdvance: boolean;
  swipeThreshold: number;
};
type SyntaxGraph = {
  version?: string;
  roots: string[];
  nodes: SyntaxNode[];
  edges: SyntaxEdge[];
};
type SyntaxNode = {
  id: string;
  kind: string;
  label?: string;
  surface?: string;
  gloss?: string;
  itemKey?: string;
  spanStart?: number;
  spanEnd?: number;
};
type SyntaxEdge = {
  source: string;
  target: string;
  relation?: string;
  label?: string;
};
type SyntaxTreeBranch = {
  node: SyntaxNode;
  relation?: string;
  children: SyntaxTreeBranch[];
};

const FLUSH_DELAY_MS = 4000;
const PROGRESS_SAVE_DELAY_MS = 1200;
const READER_POSITION_PREFIX = "tifl.reader.position.";
const READER_DISPLAY_SETTINGS_KEY = "tifl.reader.display.v1";
const AUDIO_TTS_CONCURRENCY = 4;

// 1-5 are self-rating; w/i are the well-known / ignored shortcuts. The reader
// event log wants the keystroke ("w"/"i"); the knowledge write wants the level.
const LEVELS: { value: KnowledgeLevel; event: string; label: string; hint: string }[] = [
  { value: "1", event: "1", label: "1", hint: "barely" },
  { value: "2", event: "2", label: "2", hint: "vague" },
  { value: "3", event: "3", label: "3", hint: "in context" },
  { value: "4", event: "4", label: "4", hint: "usually" },
  { value: "5", event: "5", label: "5", hint: "nearly" },
  { value: "well_known", event: "w", label: "w", hint: "known" },
  { value: "ignored", event: "i", label: "i", hint: "ignored" },
];

const MOBILE_LAYOUT_QUERY = "(max-width: 52rem)";

// Rank a word's known-ness for swipe-to-advance. A swipe stops on the first word
// whose rank is at or below the configured threshold (or at the sentence edge).
// Unseen words are always a stop (0); well_known / ignored never are.
function levelRank(level: KnowledgeLevel): number {
  switch (level) {
    case "1":
    case "2":
    case "3":
    case "4":
    case "5":
      return Number(level);
    case "well_known":
    case "ignored":
      return 99;
    default:
      return 0;
  }
}

function swipeThresholdLabel(threshold: number): string {
  if (threshold <= 0) return "new words";
  if (threshold >= 5) return "any studied word";
  return `level ${threshold} or lower`;
}

export function ReaderView(props: {
  storyId: string;
  sessionId?: string;
  title?: string;
  pendingTasks?: number;
  taskGenerationState?: "generating" | "failed";
  story?: StoryLoad | null;
  active?: boolean;
  editable?: boolean;
  canGenerateTasks?: boolean;
  onReadingStarted?: () => void;
  onStoryUpdated?: () => void;
  onTasksGenerating?: () => void;
  onOpenTasks?: () => void;
}) {
  const [status, setStatus] = createSignal<"loading" | "ready" | "error">("loading");
  const [finishStatus, setFinishStatus] = createSignal<"idle" | "saving" | "done">("idle");
  const [exitStatus, setExitStatus] = createSignal<"idle" | "saving">("idle");
  const [archiveStatus, setArchiveStatus] = createSignal<"idle" | "saving">("idle");
  const [finishedAt, setFinishedAt] = createSignal<number | undefined>();
  const [tokens, setTokens] = createSignal<StoryToken[]>([]);
  const [sentenceSpans, setSentenceSpans] = createSignal<SentenceSpan[]>([]);
  const [language, setLanguage] = createSignal("");
  const [readerDOMVersion, setReaderDOMVersion] = createSignal(0);
  const [pageWindow, setPageWindow] = createSignal<StoryPageWindow | undefined>();
  const [pageLoading, setPageLoading] = createSignal(false);
  const [infinitePageLoading, setInfinitePageLoading] = createSignal<"previous" | "next" | null>(null);
  const [loadedPagesVersion, setLoadedPagesVersion] = createSignal(0);
  const [knowledge, setKnowledge] = createStore<Record<string, ReaderKnowledge>>({});
  const [surfaceKnowledge, setSurfaceKnowledge] = createStore<Record<string, ReaderSurfaceKnowledge>>({});
  const [cursor, setCursor] = createSignal(0);
  const [popupVisible, setPopupVisible] = createSignal(false);
  const [popupPos, setPopupPos] = createSignal<{ top: number; left: number } | null>(null);
  const [lastInspectedPosition, setLastInspectedPosition] = createSignal<number | undefined>();
  const [displayPanelOpen, setDisplayPanelOpen] = createSignal(false);
  const [displaySettings, setDisplaySettings] = createStore<ReaderDisplaySettings>(readReaderDisplaySettings());
  const [audioPlaying, setAudioPlaying] = createSignal(false);
  const [audioCurrentTime, setAudioCurrentTime] = createSignal(0);
  const [audioDuration, setAudioDuration] = createSignal(0);
  const [definitions, setDefinitions] = createStore<Record<string, Loadable<Definition>>>({});
  const [definitionOptions, setDefinitionOptions] = createStore<Record<string, Loadable<DefinitionOption[]>>>({});
  const [sourcePickerKey, setSourcePickerKey] = createSignal("");
  const [audioGeneration, setAudioGeneration] = createSignal<AudioGeneration>({ status: "idle", completed: 0, total: 0 });
  const [audioCacheVersion, setAudioCacheVersion] = createSignal(0);
  const [storyEditorOpen, setStoryEditorOpen] = createSignal(false);
  const [storyEditLoading, setStoryEditLoading] = createSignal(false);
  const [storyDraft, setStoryDraft] = createSignal("");
  const [storySaving, setStorySaving] = createSignal(false);
  const [storyActionError, setStoryActionError] = createSignal("");
  const [taskSelection, setTaskSelection] = createSignal<TaskSelection | null>(null);
  const [taskGenerating, setTaskGenerating] = createSignal(false);
  // Personal-dictionary editing. Only one popup (one key) is open at a time, so
  // a single edit state suffices. `removalNotice` is keyed so the "removed"
  // message stays scoped to the word it belongs to.
  const [editing, setEditing] = createSignal(false);
  const [editGloss, setEditGloss] = createSignal("");
  const [editNotes, setEditNotes] = createSignal("");
  const [editError, setEditError] = createSignal("");
  const [actionError, setActionError] = createStore<Record<string, string | undefined>>({});
  const [removalNotice, setRemovalNotice] = createStore<Record<string, RemovalNotice | undefined>>({});
  const [analysis, setAnalysis] = createSignal<Analysis>(null);
  // Radial rate menu (mobile long-press): a scrim + a ring of level buttons
  // fanned around the word where it sits, plus a compact gloss bubble.
  const [radialMenu, setRadialMenu] = createSignal<RadialGeom | null>(null);
  // `radialHot` is the ring button (or "bubble") the finger is currently over
  // during the press-drag.
  const [radialHot, setRadialHot] = createSignal<string | null>(null);
  const [sentences, setSentences] = createStore<Record<number, Loadable<unknown>>>({});
  const [words, setWords] = createStore<Record<string, Loadable<unknown>>>({});
  // Narrow viewports get the bottom-sheet treatment: the popup, study dock, and
  // display panel dock to the bottom edge, and reading is driven by swipes.
  const mobileLayoutMedia = window.matchMedia(MOBILE_LAYOUT_QUERY);
  const [mobileLayout, setMobileLayout] = createSignal(mobileLayoutMedia.matches);

  // One element ref per word token, indexed by token position. Used to move the
  // cursor highlight surgically (two DOM writes) and to anchor the popup.
  const wordEls: (HTMLElement | undefined)[] = [];
  const tokenEls: (HTMLElement | undefined)[] = [];
  let readerTextEl: HTMLDivElement | undefined;
  let popupEl: HTMLElement | undefined;
  // Touch gesture state for the reading column: swipe left/right to advance,
  // long-press to inspect a word. Coordinates are viewport-relative.
  let touchOrigin: { x: number; y: number; time: number; position: number | undefined } | null = null;
  let longPressTimer: number | undefined;
  let gestureConsumedTap = false;
  let selectableCursorIndices: number[] = [];
  let loadedStoryPages = new Map<number, StoryLoad>();
  let scrollCursorFrame: number | undefined;
  let infiniteScrollFrame: number | undefined;
  let scrollCursorSuppressedUntil = 0;
  let preserveScrollForCursorUpdate = false;
  let pending: ReaderEvent[] = [];
  let flushTimer: number | undefined;
  let progressSaveTimer: number | undefined;
  let progressSaveChain: Promise<void> = Promise.resolve();
  let readingStart: Promise<void> | null = null;
  let dictionaryMutationSeq = 0;
  const dictionaryMutationIds: Record<string, number> = {};
  const sentenceAudioURLs = new Map<string, string>();
  const sentenceAudioRequests = new Map<string, Promise<string>>();
  const sentenceAlignments = new Map<string, ReaderWordTiming[]>();
  const sentenceAlignmentRequests = new Map<string, Promise<ReaderWordTiming[]>>();
  let sentenceAudio: HTMLAudioElement | null = null;
  let activeSentenceWords: ReaderWordTiming[] = [];
  let sentencePlaybackToken = 0;
  let audioGenerationToken = 0;
  let sentenceAnimationFrame: number | undefined;
  let speakingPosition: number | undefined;
  let disposed = false;
  let suppressFinalProgressSave = false;

  const currentToken = () => tokens()[cursor()];

  onMount(() => {
    document.addEventListener("keydown", onKeyDown);
    document.addEventListener("visibilitychange", onVisibilityChange);
    window.addEventListener("beforeunload", onBeforeUnload);
    window.addEventListener("resize", repositionPopup);
    window.addEventListener("resize", closeRadialMenu);
    window.addEventListener("scroll", repositionPopup, true);
    window.addEventListener("scroll", closeRadialMenu, true);
    window.addEventListener("scroll", scheduleMidpointCursorCapture, { passive: true });
    window.addEventListener("scroll", scheduleInfinitePageLoad, { passive: true });
    document.addEventListener("selectionchange", captureTaskSelection);
    document.addEventListener("contextmenu", onReaderContextMenu, true);
    document.addEventListener("selectstart", onReaderSelectStart, true);
    mobileLayoutMedia.addEventListener("change", onMobileLayoutChange);
    // Reader mode hides the global app chrome on small screens (CSS keys off this).
    document.body.dataset.readerActive = "";
    void load();
  });

  onCleanup(() => {
    disposed = true;
    audioGenerationToken++;
    document.removeEventListener("keydown", onKeyDown);
    document.removeEventListener("visibilitychange", onVisibilityChange);
    window.removeEventListener("beforeunload", onBeforeUnload);
    window.removeEventListener("resize", repositionPopup);
    window.removeEventListener("resize", closeRadialMenu);
    window.removeEventListener("scroll", repositionPopup, true);
    window.removeEventListener("scroll", closeRadialMenu, true);
    window.removeEventListener("scroll", scheduleMidpointCursorCapture);
    window.removeEventListener("scroll", scheduleInfinitePageLoad);
    document.removeEventListener("selectionchange", captureTaskSelection);
    document.removeEventListener("contextmenu", onReaderContextMenu, true);
    document.removeEventListener("selectstart", onReaderSelectStart, true);
    mobileLayoutMedia.removeEventListener("change", onMobileLayoutChange);
    delete document.body.dataset.readerActive;
    detachReaderTextGestures();
    if (longPressTimer !== undefined) clearTimeout(longPressTimer);
    sentencePlaybackToken++;
    sentenceAudio?.pause();
    if (sentenceAnimationFrame !== undefined) cancelAnimationFrame(sentenceAnimationFrame);
    if (scrollCursorFrame !== undefined) cancelAnimationFrame(scrollCursorFrame);
    if (infiniteScrollFrame !== undefined) cancelAnimationFrame(infiniteScrollFrame);
    clearSpeakingWord();
    for (const objectURL of sentenceAudioURLs.values()) URL.revokeObjectURL(objectURL);
    sentenceAudioURLs.clear();
    sentenceAudioRequests.clear();
    sentenceAlignments.clear();
    sentenceAlignmentRequests.clear();
    if (progressSaveTimer !== undefined) clearTimeout(progressSaveTimer);
    if (status() === "ready" && !suppressFinalProgressSave) {
      void saveProgressSnapshot(leavingProgressPosition(), false, true).catch(() => undefined);
    }
    void flush(true);
  });

  async function load() {
    try {
      if (props.sessionId) {
        readingStart = startReading(props.sessionId)
          .then(() => props.onReadingStarted?.())
          .catch(() => {
            appStore.showToast("This session could not be marked as started.", "error");
          });
      }
      const story = props.story?.story_id === props.storyId
        ? props.story
        : await getStory(props.storyId, { paged: true });
      applyStory(story);
      setStatus("ready");
    } catch {
      setStatus("error");
    }
  }

  function applyStory(story: StoryLoad, preferredPosition?: number) {
    // Seed every canonical key and exact-form key so per-span subscriptions are
    // fine-grained from the first render; untouched words stay unseen ("").
    const seeded: Record<string, ReaderKnowledge> = {};
    const seededSurface: Record<string, ReaderSurfaceKnowledge> = {};
    for (const token of story.tokens) {
      if (token.is_word && token.key && token.form_key) {
        seeded[token.key] = story.knowledge[token.key] ?? { level: "", lookup_count: 0 };
        seededSurface[token.form_key] = story.surface_knowledge[token.form_key] ?? { level: "" };
      }
    }
    setKnowledge(seeded);
    setSurfaceKnowledge(seededSurface);
    setTokens(story.tokens);
    selectableCursorIndices = story.tokens.flatMap((token, index) => isSelectableToken(token) ? [index] : []);
    setSentenceSpans(story.sentences);
    setLanguage(story.language);
    setPageWindow(story.window);
    loadedStoryPages = new Map();
    if (story.window) loadedStoryPages.set(story.window.page_index, story);
    setLoadedPagesVersion((version) => version + 1);
    const localProgress = readSavedCursor(props.storyId);
    const serverProgress = story.reading_progress;
    const resumePosition = preferredPosition ?? (localProgress && localProgress.savedAt > serverProgress.updated_at
      ? localProgress.position
      : serverProgress.position);
    setFinishedAt(serverProgress.finished_at);
    setFinishStatus(serverProgress.finished_at ? "done" : "idle");
    setCursor(resolveCursorIndex(story.tokens, resumePosition));
    queueMicrotask(() => {
      if (!disposed) {
        setReaderDOMVersion((version) => version + 1);
        scheduleInfinitePageLoad();
      }
    });
  }

  function captureTaskSelection() {
    if (!props.canGenerateTasks || storyEditorOpen() || !readerTextEl) {
      setTaskSelection(null);
      return;
    }
    const selection = window.getSelection();
    if (!selection || selection.isCollapsed || selection.rangeCount === 0 ||
      !selection.anchorNode || !selection.focusNode ||
      !readerTextEl.contains(selection.anchorNode) || !readerTextEl.contains(selection.focusNode)) {
      setTaskSelection(null);
      return;
    }
    const range = selection.getRangeAt(0);
    const positions: number[] = [];
    for (const token of tokens()) {
      const element = tokenEls[token.position];
      if (element && range.intersectsNode(element)) positions.push(token.position);
    }
    if (positions.length === 0) {
      setTaskSelection(null);
      return;
    }
    const startPosition = Math.min(...positions);
    const endPosition = Math.max(...positions) + 1;
    const wordCount = tokens().filter((token) =>
      token.position >= startPosition && token.position < endPosition && token.is_word).length;
    setTaskSelection(wordCount > 0 ? { startPosition, endPosition, wordCount } : null);
  }

  function taskTokenSelected(position: number): boolean {
    const selected = taskSelection();
    return !!selected && position >= selected.startPosition && position < selected.endPosition;
  }

  async function beginStoryEdit() {
    if (storyEditLoading()) return;
    setStoryEditLoading(true);
    setStoryActionError("");
    try {
      // Editing is a whole-document operation even when reading is paged.
      // Never seed the editor with only the visible page and overwrite a book.
      const source = pageWindow() ? await getStory(props.storyId) : undefined;
      setStoryDraft((source?.tokens ?? tokens()).map((token) => token.surface).join(""));
      setTaskSelection(null);
      window.getSelection()?.removeAllRanges();
      closeOverlays();
      setStoryEditorOpen(true);
    } catch (error) {
      setStoryActionError(readerStoryActionMessage(error, "The full story could not be loaded for editing."));
    } finally {
      setStoryEditLoading(false);
    }
  }

  function cancelStoryEdit() {
    if (storySaving()) return;
    setStoryEditorOpen(false);
    setStoryActionError("");
  }

  async function saveStoryEdit() {
    const text = storyDraft().trim();
    if (!text) {
      setStoryActionError("Story text cannot be empty.");
      return;
    }
    setStorySaving(true);
    setStoryActionError("");
    try {
      // Flush old positional events before the server removes them as part of
      // the edit reset. Never let a failed flush reinsert stale positions later.
      await flush();
      if (progressSaveTimer !== undefined) {
        clearTimeout(progressSaveTimer);
        progressSaveTimer = undefined;
      }
      await progressSaveChain.catch(() => undefined);
      await updateStory(props.storyId, { text });
      pending = [];
      suppressFinalProgressSave = true;
      clearSavedCursor(props.storyId);
      setStoryEditorOpen(false);
      props.onStoryUpdated?.();
    } catch (error) {
      setStoryActionError(readerStoryActionMessage(error, "The story could not be saved."));
    } finally {
      setStorySaving(false);
    }
  }

  async function generateTasksForSelection() {
    const selected = taskSelection();
    if (!selected || taskGenerating()) return;
    setTaskGenerating(true);
    setStoryActionError("");
    try {
      await generateStoryTasks(props.storyId, {
        start_position: selected.startPosition,
        end_position: selected.endPosition,
      });
      props.onTasksGenerating?.();
    } catch (error) {
      setStoryActionError(readerStoryActionMessage(error, "Tasks could not be generated for that selection."));
    } finally {
      setTaskGenerating(false);
    }
  }

  // Cursor highlight: clear the previous word, mark the new one. Two writes.
  let prevCursor: number | undefined;
  createEffect(() => {
    const c = cursor();
    readerDOMVersion();
    if (status() !== "ready") {
      return;
    }
    const token = tokens()[c];
    if (token) {
      writeSavedCursor(props.storyId, token.position);
      scheduleProgressSave();
    }
    if (prevCursor !== undefined && prevCursor !== c) {
      wordElementForCursor(prevCursor)?.removeAttribute("data-cursor");
    }
    const el = wordElementForCursor(c);
    if (el) {
      el.setAttribute("data-cursor", "");
      if (!preserveScrollForCursorUpdate && !wordIsComfortablyVisible(el)) {
        el.scrollIntoView({ block: "center", inline: "nearest" });
      }
    }
    preserveScrollForCursorUpdate = false;
    prevCursor = c;
  });

  createEffect(() => {
    writeReaderDisplaySettings({ ...displaySettings });
  });

  createEffect(() => {
    if (displaySettings.flowMode === "infinite" && status() === "ready") {
      queueMicrotask(scheduleInfinitePageLoad);
    }
  });

  // Session panels stay mounted while the learner moves to tasks. Persist the
  // bookmark at that boundary even though the reader component is not cleaned
  // up and the ordinary debounce would probably save it shortly afterward.
  createEffect(() => {
    if (props.active === false && status() === "ready") {
      void queueProgressSave(leavingProgressPosition()).catch(() => undefined);
    }
  });

  // On mobile a bottom sheet covers the lower half, so keep the studied word in
  // the strip of text left visible between the top bar and the sheet.
  function scrollCursorIntoReadableBand() {
    if (!mobileLayout() || status() !== "ready") return;
    const el = wordElementForCursor(cursor());
    if (!el) return;
    const sheet = document.querySelector<HTMLElement>(".reader-study-dock, .reader-popup");
    const topBar = document.querySelector<HTMLElement>(".reader-topbar");
    const bandTop = (topBar?.getBoundingClientRect().bottom ?? 0) + 12;
    const bandBottom = (sheet?.getBoundingClientRect().top ?? window.innerHeight * 0.55) - 12;
    if (bandBottom - bandTop < 48) return;
    const rect = el.getBoundingClientRect();
    const wordCenter = rect.top + rect.height / 2;
    if (wordCenter < bandTop + 8 || wordCenter > bandBottom - 8) {
      suppressMidpointCursorCapture();
      preserveScrollForCursorUpdate = true;
      window.scrollBy(0, wordCenter - (bandTop + bandBottom) / 2);
    }
  }

  createEffect(() => {
    const sheetOpen = analysis() !== null || (popupVisible() && displaySettings.popupEnabled);
    cursor();
    if (!sheetOpen || !mobileLayout() || status() !== "ready") return;
    requestAnimationFrame(() => requestAnimationFrame(scrollCursorIntoReadableBand));
  });

  // The lightweight popup follows the cursor. A Word dock that is already open
  // follows it too; once the popup closes, the dock stays pinned to the last
  // inspected word while reading continues.
  createEffect(() => {
    const visible = popupVisible();
    const c = cursor();
    if (!visible || status() !== "ready") {
      return;
    }
    const token = tokens()[c];
    if (!token?.key) {
      return;
    }
    setLastInspectedPosition(token.position);
    logLookup(token.position, token.key);
    void ensureDefinition(token.key);
    void ensureWord(token.key);
    const activeStudy = analysis();
    if (activeStudy?.mode === "word" && activeStudy.key !== token.key) {
      setAnalysis({ mode: "word", key: token.key });
    }
    repositionPopup();
  });

  function moveCursor(direction: 1 | -1) {
    if (editing()) {
      return; // edit mode pins the popup to its word
    }
    const list = tokens();
    for (let i = cursor() + direction; i >= 0 && i < list.length; i += direction) {
      if (list[i].is_word && list[i].key) {
        suppressMidpointCursorCapture();
        setCursor(i);
        return;
      }
    }
    if (displaySettings.flowMode === "infinite") {
      void loadInfiniteStoryPage(direction, true);
      return;
    }
    const currentWindow = pageWindow();
    if ((direction === 1 && currentWindow?.has_next) || (direction === -1 && currentWindow?.has_previous)) {
      void navigatePage(direction);
    }
  }

  function setReaderCursorPosition(position: number) {
    if (editing()) {
      return; // edit mode pins the popup to its word
    }
    suppressMidpointCursorCapture();
    setCursor(resolveCursorIndex(tokens(), position));
  }

  function onMobileLayoutChange(event: MediaQueryListEvent) {
    setMobileLayout(event.matches);
    if (event.matches) {
      setPopupPos(null); // the JS anchor is desktop-only; the sheet is CSS-driven
    } else {
      repositionPopup();
    }
  }

  // Swipe-to-advance: move the cursor forward/back to the next word worth a pause
  // — the first one at or below the knowledge threshold — but never past the end
  // (or start) of the current sentence. When already at the sentence edge, fall
  // through to the ordinary word step so page turns still work.
  function swipeAdvance(direction: 1 | -1) {
    if (editing() || storyEditorOpen() || status() !== "ready") return;
    const list = tokens();
    const start = cursor();
    const span = currentSentenceSpan();
    let target = start;
    for (let i = start + direction; i >= 0 && i < list.length; i += direction) {
      const token = list[i];
      if (span) {
        if (direction === 1 && token.position >= span.end_position) break;
        if (direction === -1 && token.position < span.start_position) break;
      }
      if (!token.is_word || !token.key) continue;
      target = i;
      if (levelRank(displayLevel(token)) <= displaySettings.swipeThreshold) break;
    }
    if (target !== start) {
      suppressMidpointCursorCapture();
      setCursor(target);
      hapticTick();
      return;
    }
    moveCursor(direction);
  }

  function wordPositionAtPoint(x: number, y: number): number | undefined {
    const el = document.elementFromPoint(x, y)?.closest<HTMLElement>(".reader-word");
    const raw = el?.dataset.pos;
    return raw === undefined ? undefined : Number(raw);
  }

  function clearLongPress() {
    if (longPressTimer !== undefined) {
      clearTimeout(longPressTimer);
      longPressTimer = undefined;
    }
  }

  function onReaderTouchStart(event: TouchEvent) {
    if (event.touches.length !== 1 || editing() || storyEditorOpen() || status() !== "ready") {
      touchOrigin = null;
      return;
    }
    const touch = event.touches[0];
    const position = wordPositionAtPoint(touch.clientX, touch.clientY);
    touchOrigin = { x: touch.clientX, y: touch.clientY, time: performance.now(), position };
    gestureConsumedTap = false;
    clearLongPress();
    if (position !== undefined) {
      longPressTimer = window.setTimeout(() => {
        longPressTimer = undefined;
        if (!touchOrigin) return;
        gestureConsumedTap = true;
        setReaderCursorPosition(position);
        if (mobileLayout()) openRadialMenu(position);
        else if (displaySettings.popupEnabled) setPopupVisible(true);
        else inspectCurrentWord();
        hapticTick();
      }, 450);
    }
  }

  function onReaderTouchMove(event: TouchEvent) {
    if (!touchOrigin) return;
    const touch = event.touches[0];
    // Once the radial menu is up, the same finger drags across it to pick a
    // rating; moves no longer scroll or cancel anything.
    if (radialMenu()) {
      event.preventDefault();
      updateRadialHot(touch.clientX, touch.clientY);
      return;
    }
    const dx = touch.clientX - touchOrigin.x;
    const dy = touch.clientY - touchOrigin.y;
    // While the hold is still pending on a word, swallow tiny moves so the OS
    // never starts a text selection under the finger.
    if (touchOrigin.position !== undefined && longPressTimer !== undefined &&
      Math.abs(dx) < 12 && Math.abs(dy) < 12) {
      event.preventDefault();
    }
    if (Math.abs(dx) > 8 || Math.abs(dy) > 8) clearLongPress();
    if (!gestureConsumedTap && displaySettings.swipeAdvance &&
      Math.abs(dx) > 44 && Math.abs(dx) > Math.abs(dy) * 1.6) {
      gestureConsumedTap = true;
      event.preventDefault(); // claim the gesture from native scroll/selection
      swipeAdvance(dx > 0 ? 1 : -1);
    }
  }

  // Pie-slice hit test: once the drag point leaves the word box, each button
  // owns an angular wedge back to the word centre. Nothing is hot while the
  // point is still over the word, or once it goes past the arch.
  function updateRadialHot(x: number, y: number) {
    const menu = radialMenu();
    if (!menu) return;
    if (document.elementFromPoint(x, y)?.closest(".reader-radial-bubble")) {
      setRadialHot("bubble");
      return;
    }
    // "In the word" is measured from where the finger actually pressed (the
    // clone stays there); the arch geometry is measured from the cluster centre.
    if (Math.abs(x - menu.pressX) <= menu.wordHalfW + 8 && Math.abs(y - menu.pressY) <= menu.wordHalfH + 8) {
      setRadialHot(null);
      return;
    }
    const rx = x - menu.cx;
    const ry = y - menu.cy;
    if (Math.hypot(rx, ry) > menu.maxReach) {
      setRadialHot(null);
      return;
    }
    const dir = menu.side === "above" ? -1 : 1;
    if (Math.sign(ry) === -dir && Math.abs(ry) > menu.wordHalfH + 6) {
      setRadialHot(null);
      return;
    }
    const phi = Math.atan2(ry, rx);
    let best = -1;
    let bestDiff = Infinity;
    for (let i = 0; i < menu.buttons.length; i++) {
      let diff = Math.abs(phi - menu.buttons[i].angle);
      if (diff > Math.PI) diff = 2 * Math.PI - diff;
      if (diff < bestDiff) {
        bestDiff = diff;
        best = i;
      }
    }
    const slice = Math.abs(menu.buttons[0].angle - menu.buttons[1].angle);
    setRadialHot(best >= 0 && bestDiff <= slice * 0.85 ? menu.buttons[best].event : null);
  }

  function onReaderTouchEnd() {
    clearLongPress();
    // Press-and-hold radial: release over a rating selects it; release into the
    // bubble opens the full word sheet; release anywhere else dismisses it.
    if (radialMenu()) {
      const hot = radialHot();
      const level = hot ? LEVELS.find((entry) => entry.event === hot) : undefined;
      touchOrigin = null;
      gestureConsumedTap = false;
      if (level) rateFromRadial(level.value, level.event);
      else if (hot === "bubble") openWordStudyFromRadial();
      else closeRadialMenu();
      swallowNextReaderClick();
      return;
    }
    const consumed = gestureConsumedTap;
    touchOrigin = null;
    if (!consumed) return;
    swallowNextReaderClick();
  }

  // Chrome fires a native contextmenu (the long-press callout) on a touch hold;
  // kill it inside the reader on touch layouts so long-press is only ours.
  function onReaderContextMenu(event: Event) {
    if (!mobileLayout()) return;
    const target = event.target as HTMLElement | null;
    if (target?.closest?.(".reader-view")) event.preventDefault();
  }

  // A long-press also tries to start a text selection. Block it inside the
  // reader on touch layouts unless the press began on the blank space between
  // words (where passage selection is still allowed).
  function onReaderSelectStart(event: Event) {
    if (!mobileLayout()) return;
    const target = event.target as HTMLElement | null;
    if (!target?.closest?.(".reader-view")) return;
    const startedOnGap = touchOrigin != null && touchOrigin.position === undefined &&
      !!target.closest?.(".reader-text");
    if (!startedOnGap) event.preventDefault();
  }

  // Swallow the click the browser synthesizes after a long-press / swipe so it
  // does not also move the cursor to wherever the finger lifted.
  function swallowNextReaderClick() {
    const swallow = (click: Event) => {
      click.stopPropagation();
      click.preventDefault();
    };
    readerTextEl?.addEventListener("click", swallow, { capture: true, once: true });
    window.setTimeout(() => readerTextEl?.removeEventListener("click", swallow, true), 350);
  }

  function attachReaderTextGestures(element: HTMLElement) {
    element.addEventListener("touchstart", onReaderTouchStart, { passive: true });
    element.addEventListener("touchmove", onReaderTouchMove, { passive: false });
    element.addEventListener("touchend", onReaderTouchEnd, { passive: true });
    element.addEventListener("touchcancel", onReaderTouchEnd, { passive: true });
  }

  function detachReaderTextGestures() {
    if (!readerTextEl) return;
    readerTextEl.removeEventListener("touchstart", onReaderTouchStart);
    readerTextEl.removeEventListener("touchmove", onReaderTouchMove);
    readerTextEl.removeEventListener("touchend", onReaderTouchEnd);
    readerTextEl.removeEventListener("touchcancel", onReaderTouchEnd);
  }

  function closeMobileSheets() {
    setPopupVisible(false);
    setAnalysis(null);
    setDisplayPanelOpen(false);
  }

  function openRadialMenu(position: number) {
    const token = tokens().find((candidate) => candidate.position === position);
    if (!token?.key) return;
    const el = wordEls[position] ?? tokenEls[position];
    if (!el) return;
    const rect = el.getBoundingClientRect();
    preserveScrollForCursorUpdate = true; // the word is under the finger; don't scroll it
    suppressMidpointCursorCapture();
    setReaderCursorPosition(position);
    setLastInspectedPosition(position);
    logLookup(position, token.key);
    void ensureDefinition(token.key);
    setRadialHot(null);

    // Squashed-dome arch: endcap buttons sit ~half a button off the word's
    // sides; the apex only rises far enough to leave a half-button gap up to
    // the gloss bubble, which sits on the same (roomier) side as the arch.
    const BTN = 28;
    const vw = window.innerWidth;
    const vh = window.innerHeight;
    const wordHalfW = Math.max(rect.width, 14) / 2;
    const wordHalfH = Math.max(rect.height, 16) / 2;
    const cx0 = rect.left + rect.width / 2;
    const cy0 = rect.top + rect.height / 2;
    const ampX = wordHalfW + BTN * 1.9;
    const archH = BTN * 1.35;
    const bubbleReserve = 96;
    // The bubble + arch stack above the word by default; flip below only if
    // that stack would run off the top.
    const side: "above" | "below" = cy0 - (archH + BTN + bubbleReserve) < 52 ? "below" : "above";
    const dir = side === "above" ? -1 : 1;
    // 1–5 ride a flat dome that hugs the word; w / i flank the dome ends at the
    // word's own level.
    const numeric = LEVELS.filter((entry) => /^[1-5]$/.test(entry.label));
    const specials = LEVELS.filter((entry) => !/^[1-5]$/.test(entry.label));
    const mk = (entry: (typeof LEVELS)[number], x: number, y: number): RadialButton => ({
      event: entry.event, value: entry.value, label: entry.label, hint: entry.hint,
      level: dataLevel(entry.value), x, y, angle: Math.atan2(y, x),
    });
    const buttons: RadialButton[] = numeric.map((entry, i) => {
      const bx = -ampX + (2 * ampX) * (i / (numeric.length - 1));
      const by = dir * archH * Math.sqrt(Math.max(0, 1 - (bx / ampX) ** 2));
      return mk(entry, bx, by);
    });
    // w / i hug the word's own sides, just off the edge, at its baseline.
    const sideX = wordHalfW + BTN * 0.8;
    specials.forEach((entry, i) => buttons.push(mk(entry, i === 0 ? -sideX : sideX, dir * BTN * 0.1)));

    // Minimal nudge so the cluster + bubble stay on screen.
    const farY = dir * (archH + BTN + bubbleReserve);
    let offY = 0;
    if (cy0 + Math.min(0, farY) - BTN / 2 < 52) offY = 52 - (cy0 + Math.min(0, farY) - BTN / 2);
    else if (cy0 + Math.max(0, farY) + BTN / 2 > vh - 74) offY = (vh - 74) - (cy0 + Math.max(0, farY) + BTN / 2);
    let offX = 0;
    if (cx0 - ampX - BTN / 2 < 10) offX = 10 - (cx0 - ampX - BTN / 2);
    else if (cx0 + ampX + BTN / 2 > vw - 10) offX = (vw - 10) - (cx0 + ampX + BTN / 2);
    const cx = cx0 + offX;
    const cy = cy0 + offY;
    const bubbleW = Math.min(272, vw - 16);

    setRadialMenu({
      position,
      key: token.key,
      surface: token.surface,
      level: dataLevel(displayLevel(token)),
      pressX: cx0,
      pressY: cy0,
      cx,
      cy,
      wordHalfW,
      wordHalfH,
      side,
      buttons,
      bubbleX: Math.min(Math.max(cx, bubbleW / 2 + 8), vw - bubbleW / 2 - 8),
      bubbleY: cy + dir * (archH + BTN),
      maxReach: ampX + BTN * 1.3,
    });
  }

  function closeRadialMenu() {
    setRadialMenu(null);
    setRadialHot(null);
  }

  function rateFromRadial(level: KnowledgeLevel, eventValue: string) {
    const menu = radialMenu();
    if (!menu) return;
    const token = tokens().find((candidate) => candidate.position === menu.position);
    if (token) rate(level, eventValue, token);
    hapticTick();
    closeRadialMenu();
  }

  function radialGloss(): string {
    const menu = radialMenu();
    if (!menu) return "";
    const entry = definitions[menu.key];
    if (entry === "loading" || !entry) return "Looking up…";
    if (entry === "error") return "No definition available.";
    return entry.gloss;
  }

  function surfaceLevelAt(position: number): KnowledgeLevel {
    const token = tokens().find((candidate) => candidate.position === position);
    return token ? surfaceLevel(token) : "";
  }

  function openWordStudyFromRadial() {
    const menu = radialMenu();
    if (!menu) return;
    closeRadialMenu();
    setAnalysis({ mode: "word", key: menu.key });
    void ensureDefinition(menu.key);
    void ensureWord(menu.key);
  }

  function scheduleMidpointCursorCapture() {
    if (scrollCursorFrame !== undefined || performance.now() < scrollCursorSuppressedUntil ||
      status() !== "ready" || storyEditorOpen() ||
      // A sheet pins the cursor to the studied word; scroll must not steal it.
      analysis() !== null || popupVisible()) return;
    scrollCursorFrame = requestAnimationFrame(() => {
      scrollCursorFrame = undefined;
      const next = viewportMidpointCursorIndex();
      if (next === undefined || next === cursor()) return;
      preserveScrollForCursorUpdate = true;
      setCursor(next);
    });
  }

  function suppressMidpointCursorCapture() {
    scrollCursorSuppressedUntil = performance.now() + 250;
    if (scrollCursorFrame !== undefined) {
      cancelAnimationFrame(scrollCursorFrame);
      scrollCursorFrame = undefined;
    }
  }

  async function navigatePage(direction: 1 | -1) {
    const currentWindow = pageWindow();
    if (!currentWindow || pageLoading() ||
      (direction === 1 && !currentWindow.has_next) ||
      (direction === -1 && !currentWindow.has_previous)) return;
    setPageLoading(true);
    setStoryActionError("");
    try {
      await queueProgressSave(leavingProgressPosition());
      const position = direction === 1
        ? currentWindow.end_position
        : Math.max(0, currentWindow.start_position - 1);
      const story = await getStory(props.storyId, { paged: true, position });
      const preferred = direction === 1
        ? firstSelectablePosition(story.tokens)
        : lastSelectablePosition(story.tokens);
      setTaskSelection(null);
      window.getSelection()?.removeAllRanges();
      closeOverlays();
      suppressMidpointCursorCapture();
      applyStory(story, preferred);
    } catch (error) {
      setStoryActionError(readerStoryActionMessage(error, "That page could not be loaded."));
    } finally {
      setPageLoading(false);
    }
  }

  function scheduleInfinitePageLoad() {
    if (infiniteScrollFrame !== undefined || displaySettings.flowMode !== "infinite" ||
      status() !== "ready" || storyEditorOpen() || !readerTextEl) return;
    infiniteScrollFrame = requestAnimationFrame(() => {
      infiniteScrollFrame = undefined;
      if (displaySettings.flowMode !== "infinite" || !readerTextEl || infinitePageLoading()) return;
      const pages = sortedLoadedStoryPages();
      const first = pages[0]?.window;
      const last = pages.at(-1)?.window;
      if (!first || !last) return;
      const rect = readerTextEl.getBoundingClientRect();
      const threshold = Math.min(1_200, window.innerHeight * 1.25);
      if (last.has_next && rect.bottom-window.innerHeight < threshold) {
        void loadInfiniteStoryPage(1);
      } else if (first.has_previous && rect.top > -threshold) {
        void loadInfiniteStoryPage(-1);
      }
    });
  }

  async function loadInfiniteStoryPage(direction: 1 | -1, moveIntoPage = false) {
    if (displaySettings.flowMode !== "infinite" || infinitePageLoading()) return;
    const pages = sortedLoadedStoryPages();
    const edge = direction === 1 ? pages.at(-1)?.window : pages[0]?.window;
    if (!edge || (direction === 1 ? !edge.has_next : !edge.has_previous)) return;
    setInfinitePageLoading(direction === 1 ? "next" : "previous");
    setStoryActionError("");
    try {
      const position = direction === 1 ? edge.end_position : Math.max(0, edge.start_position - 1);
      const story = await getStory(props.storyId, { paged: true, position });
      if (disposed || displaySettings.flowMode !== "infinite") return;
      if (!story.window) throw new Error("The server returned an unpaged story response.");
      loadedStoryPages.set(story.window.page_index, story);
      while (loadedStoryPages.size > 3) {
        const loaded = sortedLoadedStoryPages();
        const discard = direction === 1 ? loaded[0] : loaded.at(-1);
        if (!discard?.window) break;
        loadedStoryPages.delete(discard.window.page_index);
      }
      setLoadedPagesVersion((version) => version + 1);
      const preferred = moveIntoPage
        ? direction === 1 ? firstSelectablePosition(story.tokens) : lastSelectablePosition(story.tokens)
        : undefined;
      mergeLoadedStoryPages(preferred);
    } catch (error) {
      setStoryActionError(readerStoryActionMessage(error, "The next part of this story could not be loaded."));
    } finally {
      setInfinitePageLoading(null);
      queueMicrotask(scheduleInfinitePageLoad);
    }
  }

  function sortedLoadedStoryPages(): PagedStoryLoad[] {
    loadedPagesVersion();
    return [...loadedStoryPages.values()]
      .filter((story): story is PagedStoryLoad => Boolean(story.window))
      .sort((left, right) => left.window.page_index - right.window.page_index);
  }

  function mergeLoadedStoryPages(preferredPosition?: number) {
    const pages = sortedLoadedStoryPages();
    if (pages.length === 0) return;
    const anchorPosition = currentToken()?.position;
    const anchorTop = anchorPosition === undefined ? undefined : wordEls[anchorPosition]?.getBoundingClientRect().top;
    readerTextEl?.querySelector("[data-cursor]")?.removeAttribute("data-cursor");

    const mergedTokens = pages.flatMap((story) => story.tokens);
    const sentenceByStart = new Map<number, SentenceSpan>();
    const mergedKnowledge: Record<string, ReaderKnowledge> = {};
    const mergedSurfaceKnowledge: Record<string, ReaderSurfaceKnowledge> = {};
    for (const story of pages) {
      for (const span of story.sentences) sentenceByStart.set(span.start_position, span);
      Object.assign(mergedKnowledge, story.knowledge);
      Object.assign(mergedSurfaceKnowledge, story.surface_knowledge);
    }
    const mergedSentences = [...sentenceByStart.values()].sort((left, right) => left.start_position - right.start_position);
    const nextPosition = preferredPosition ?? anchorPosition ?? firstSelectablePosition(mergedTokens);

    setKnowledge(mergedKnowledge);
    setSurfaceKnowledge(mergedSurfaceKnowledge);
    setTokens(mergedTokens);
    selectableCursorIndices = mergedTokens.flatMap((token, index) => isSelectableToken(token) ? [index] : []);
    setSentenceSpans(mergedSentences);
    setPageWindow(pages.find((story) => story.window && nextPosition >= story.window.start_position &&
      nextPosition < story.window.end_position)?.window ?? pages[0].window);
    preserveScrollForCursorUpdate = preferredPosition === undefined;
    setCursor(resolveCursorIndex(mergedTokens, nextPosition));
    setTaskSelection(null);
    window.getSelection()?.removeAllRanges();

    queueMicrotask(() => {
      if (disposed) return;
      if (preferredPosition === undefined && anchorPosition !== undefined && anchorTop !== undefined) {
        const nextTop = wordEls[anchorPosition]?.getBoundingClientRect().top;
        if (nextTop !== undefined) window.scrollBy(0, nextTop - anchorTop);
        preserveScrollForCursorUpdate = true;
      }
      setReaderDOMVersion((version) => version + 1);
      scheduleInfinitePageLoad();
    });
  }

  async function changeReaderFlowMode(mode: ReaderDisplaySettings["flowMode"]) {
    if (mode === displaySettings.flowMode || pageLoading()) return;
    if (mode === "infinite") {
      setDisplaySettings("flowMode", mode);
      queueMicrotask(scheduleInfinitePageLoad);
      return;
    }
    setDisplaySettings("flowMode", mode);
    setPageLoading(true);
    setStoryActionError("");
    try {
      const position = leavingProgressPosition();
      const story = await getStory(props.storyId, { paged: true, position });
      suppressMidpointCursorCapture();
      applyStory(story, position);
    } catch (error) {
      setDisplaySettings("flowMode", "infinite");
      setStoryActionError(readerStoryActionMessage(error, "Page mode could not be restored."));
    } finally {
      setPageLoading(false);
    }
  }

  function infiniteStoryHasNext(): boolean {
    return sortedLoadedStoryPages().at(-1)?.window?.has_next ?? false;
  }

  function viewportMidpointCursorIndex(): number | undefined {
    if (!readerTextEl || selectableCursorIndices.length === 0) return undefined;
    const textRect = readerTextEl.getBoundingClientRect();
    const topBoundary = readerViewportTopBoundary();
    if (textRect.bottom <= topBoundary || textRect.top >= window.innerHeight) return undefined;
    const targetY = Math.min(
      Math.max(window.innerHeight / 2, Math.max(textRect.top, topBoundary)),
      Math.min(textRect.bottom, window.innerHeight),
    );

    let low = 0;
    let high = selectableCursorIndices.length;
    while (low < high) {
      const middle = Math.floor((low + high) / 2);
      const element = wordElementForCursor(selectableCursorIndices[middle]);
      const rect = element?.getBoundingClientRect();
      const center = rect ? rect.top + rect.height / 2 : Number.POSITIVE_INFINITY;
      if (center < targetY) low = middle + 1;
      else high = middle;
    }

    let best: number | undefined;
    let bestDistance = Number.POSITIVE_INFINITY;
    for (const candidate of [low - 2, low - 1, low, low + 1]) {
      if (candidate < 0 || candidate >= selectableCursorIndices.length) continue;
      const index = selectableCursorIndices[candidate];
      const element = wordElementForCursor(index);
      if (!element) continue;
      const rect = element.getBoundingClientRect();
      const distance = Math.abs(rect.top + rect.height / 2 - targetY);
      if (distance < bestDistance) {
        best = index;
        bestDistance = distance;
      }
    }
    return best;
  }

  function wordIsComfortablyVisible(element: HTMLElement): boolean {
    const rect = element.getBoundingClientRect();
    return rect.top >= readerViewportTopBoundary() && rect.bottom <= window.innerHeight - 16;
  }

  function readerViewportTopBoundary(): number {
    return Math.min(96, window.innerHeight * 0.2);
  }

  function wordElementForCursor(index: number): HTMLElement | undefined {
    const token = tokens()[index];
    return token ? wordEls[token.position] : undefined;
  }

  function togglePopup() {
    if (!currentToken()?.key) {
      return;
    }
    if (!displaySettings.popupEnabled) {
      if (analysis()?.mode === "word") inspectCurrentWord();
      return;
    }
    setPopupVisible((visible) => !visible);
  }

  function inspectCurrentWord() {
    const token = currentToken();
    if (!token?.key) return;
    setLastInspectedPosition(token.position);
    setAnalysis({ mode: "word", key: token.key });
    void ensureDefinition(token.key);
    void ensureWord(token.key);
  }

  function openRememberedWord() {
    const position = lastInspectedPosition() ?? currentToken()?.position;
    const token = tokens().find((candidate) => candidate.position === position && candidate.key);
    if (!token?.key) return;
    setAnalysis({ mode: "word", key: token.key });
    void ensureDefinition(token.key);
    void ensureWord(token.key);
  }

  function repositionPopup() {
    if (!popupVisible()) return;
    if (mobileLayout()) return; // the popup is a bottom sheet here; CSS positions it
    const el = wordElementForCursor(cursor());
    if (!el) {
      setPopupPos(null);
      return;
    }
    const rect = el.getBoundingClientRect();
    const popupWidth = popupEl?.offsetWidth ?? 320;
    const left = Math.max(8, Math.min(rect.left, window.innerWidth - 8 - popupWidth));
    const height = popupEl?.offsetHeight ?? 0;
    let top = rect.bottom + 8;
    if (height > 0 && top + height > window.innerHeight - 8) {
      const above = rect.top - 8 - height;
      top = above >= 8 ? above : Math.max(8, window.innerHeight - 8 - height);
    }
    setPopupPos({ top, left });
  }

  const popupStyle = (): JSX.CSSProperties => {
    if (mobileLayout()) return {};
    const pos = popupPos();
    return pos ? { top: `${pos.top}px`, left: `${pos.left}px` } : { visibility: "hidden" };
  };

  async function ensureDefinition(key: string) {
    if (definitions[key] && definitions[key] !== "error") {
      return;
    }
    setDefinitions(key, "loading");
    try {
      setDefinitions(key, await getDefinition(props.storyId, key));
    } catch {
      setDefinitions(key, "error");
    }
  }

  function toggleDefinitionSources(key: string) {
    if (sourcePickerKey() === key) {
      setSourcePickerKey("");
      return;
    }
    setSourcePickerKey(key);
    if (!definitionOptions[key] || definitionOptions[key] === "error") {
      setDefinitionOptions(key, "loading");
      void getDefinitionOptions(props.storyId, key)
        .then((options) => setDefinitionOptions(key, options))
        .catch(() => setDefinitionOptions(key, "error"));
    }
  }

  function chooseDefinitionSource(key: string, option: DefinitionOption) {
    const current = loadedDefinition(key);
    setDefinitions(key, {
      key: option.key,
      source: option.source,
      gloss: option.gloss,
      grammatical_note: option.grammatical_note,
      example: option.example,
      etymology: option.etymology,
      notes: option.notes,
      trace: {
        query_key: key,
        resolved_key: option.key,
        winning_source: option.source,
        steps: current?.trace.steps ?? [],
      },
    });
    setSourcePickerKey("");
  }

  // ---- personal dictionary editing --------------------------------------
  function loadedDefinition(key: string): Definition | null {
    const entry = definitions[key];
    return entry && entry !== "loading" && entry !== "error" ? entry : null;
  }

  function startEdit(key: string) {
    const def = loadedDefinition(key);
    setEditGloss(def?.gloss ?? "");
    setEditNotes(def?.notes ?? "");
    setEditError("");
    setActionError(key, undefined);
    setRemovalNotice(key, undefined);
    setEditing(true);
  }

  function cancelEdit() {
    setEditing(false);
    setEditError("");
  }

  function saveEdit(key: string) {
    const gloss = editGloss().trim();
    if (!gloss) {
      setEditError("empty definition — use Remove instead");
      return;
    }
    const notes = editNotes().trim() || undefined;
    const previous = snapshotDefinition(definitions[key]);
    const base = loadedDefinition(key);
    const mutationID = beginDictionaryMutation(key);
    const optimistic: Definition = {
      key,
      source: "user",
      gloss,
      notes,
      grammatical_note: base?.grammatical_note,
      example: base?.example,
      etymology: base?.etymology,
      trace: {
        query_key: base?.trace.query_key ?? key,
        resolved_key: base?.trace.resolved_key ?? key,
        winning_source: "user",
        steps: base?.trace.steps ?? [],
      },
    };
    setDefinitions(key, optimistic);
    setActionError(key, undefined);
    setRemovalNotice(key, undefined);
    setEditing(false);
    setEditError("");
    void putDictionaryEntry({ language: language(), key, gloss, notes }).catch(() => {
      if (isCurrentDictionaryMutation(key, mutationID)) {
        setDefinitions(key, previous);
        setActionError(key, "Couldn't save — try again.");
      }
    });
  }

  function removeEntry(key: string) {
    const previous = snapshotDefinition(definitions[key]);
    const mutationID = beginDictionaryMutation(key);
    setEditing(false);
    setEditError("");
    setActionError(key, undefined);
    setRemovalNotice(key, "removed");
    setDefinitions(key, "loading");
    void deleteDictionaryEntry(language(), key)
      .then(() => refetchDefinition(key, mutationID))
      .catch(() => {
        if (isCurrentDictionaryMutation(key, mutationID)) {
          setDefinitions(key, previous);
          setRemovalNotice(key, undefined);
          setActionError(key, "Couldn't remove your definition — try again.");
        }
      });
  }

  async function refetchDefinition(key: string, mutationID = beginDictionaryMutation(key)) {
    setActionError(key, undefined);
    setRemovalNotice(key, "removed");
    setDefinitions(key, "loading");
    try {
      const definition = await getDefinition(props.storyId, key);
      if (isCurrentDictionaryMutation(key, mutationID)) {
        setDefinitions(key, definition);
        setRemovalNotice(key, "removed");
      }
    } catch {
      if (isCurrentDictionaryMutation(key, mutationID)) {
        setDefinitions(key, "error");
        setRemovalNotice(key, "removed-failed");
      }
    }
  }

  function beginDictionaryMutation(key: string): number {
    const mutationID = ++dictionaryMutationSeq;
    dictionaryMutationIds[key] = mutationID;
    return mutationID;
  }

  function isCurrentDictionaryMutation(key: string, mutationID: number): boolean {
    return dictionaryMutationIds[key] === mutationID;
  }

  function snapshotDefinition(entry: Loadable<Definition> | undefined): Loadable<Definition> {
    if (!entry) {
      return "error";
    }
    if (entry === "loading" || entry === "error") {
      return entry;
    }
    return {
      ...entry,
      trace: {
        ...entry.trace,
        steps: [...entry.trace.steps],
      },
    };
  }

  // Entering edit mode (and validation errors) change the popup's size; re-anchor
  // so it never spills off the bottom of the viewport.
  createEffect(() => {
    editing();
    editError();
    editGloss();
    editNotes();
    sourcePickerKey();
    const token = currentToken();
    if (token?.key) {
      definitions[token.key];
      definitionOptions[token.key];
      actionError[token.key];
      removalNotice[token.key];
    }
    if (popupVisible()) {
      window.requestAnimationFrame(repositionPopup);
    }
  });

  createEffect(() => {
    const key = currentToken()?.key ?? "";
    if (sourcePickerKey() && sourcePickerKey() !== key) setSourcePickerKey("");
  });

  // Closing the popup drops any half-finished edit so it does not resurface.
  createEffect(() => {
    if (!popupVisible()) {
      setEditing(false);
      setEditError("");
      setSourcePickerKey("");
    }
  });

  function definitionArea(key: string, compactGrammar = false): JSX.Element {
    if (editing()) {
      const current = loadedDefinition(key);
      return (
        <form
          class="reader-def-edit-form"
          onSubmit={(event) => {
            event.preventDefault();
            saveEdit(key);
          }}
        >
          <label class="reader-field">
            Definition
            <input
              class="reader-field-input"
              value={editGloss()}
              autocomplete="off"
              ref={(el) => window.setTimeout(() => el.focus())}
              onInput={(event) => {
                setEditGloss(event.currentTarget.value);
                setEditError("");
              }}
            />
          </label>
          <label class="reader-field">
            Notes (optional)
            <textarea
              class="reader-field-notes"
              rows={4}
              value={editNotes()}
              onInput={(event) => setEditNotes(event.currentTarget.value)}
            />
          </label>
          <Show when={editError()}>
            {(message) => <p class="reader-field-error" role="alert">{message()}</p>}
          </Show>
          <div class="reader-edit-actions">
            <button class="primary-button reader-edit-save" type="submit">Save</button>
            <button class="secondary-button reader-edit-cancel" type="button" onClick={cancelEdit}>Cancel</button>
          </div>
          <Show when={current?.source === "user"}>
            <button class="reader-remove-link" type="button" onClick={() => removeEntry(key)}>
              Remove my definition
            </button>
          </Show>
        </form>
      );
    }

    const entry = definitions[key];
    const notice = removalNotice[key];
    const error = actionError[key];
    if (notice === "removed-failed") {
      return (
        <div class="reader-popup-def">
          <div class="reader-def-head">
            <button class="reader-def-edit" type="button" aria-label="Edit definition" title="Edit definition" onClick={() => startEdit(key)}>
              <span aria-hidden="true">✎</span>
            </button>
          </div>
          <p class="reader-popup-notice" role="status">Your definition was removed — couldn't load a definition.</p>
          <button class="secondary-button reader-retry-definition" type="button" onClick={() => void refetchDefinition(key)}>
            Retry
          </button>
        </div>
      );
    }

    return (
      <div class="reader-popup-def">
        <div class="reader-def-head">
          <Show when={loadedDefinition(key)?.source === "user"}>
            <span class="reader-def-mine">Your definition</span>
          </Show>
          <button class="reader-def-edit" type="button" aria-label="Edit definition" title="Edit definition" onClick={() => startEdit(key)}>
            <span aria-hidden="true">✎</span>
          </button>
        </div>
        <Show when={error}>
          {(message) => <p class="reader-popup-error" role="alert">{message()}</p>}
        </Show>
        <Show when={notice === "removed"}>
          <p class="reader-popup-notice" role="status">Your definition was removed — showing dictionary definition.</p>
        </Show>
        {definitionBody(entry, language(), !compactGrammar)}
        <Show when={loadedDefinition(key)}>
          <button class="reader-source-picker-toggle" type="button" onClick={() => toggleDefinitionSources(key)}>
            {sourcePickerKey() === key ? "Hide sources" : "Pick different source"}
          </button>
        </Show>
        <Show when={sourcePickerKey() === key}>
          {definitionSourcePicker(definitionOptions[key], loadedDefinition(key), (option) => chooseDefinitionSource(key, option))}
        </Show>
      </div>
    );
  }

  function rate(level: KnowledgeLevel, eventValue: string, target?: StoryToken) {
    const token = target ?? currentToken();
    if (!token?.key || !token.form_key || !token.surface_key) {
      return;
    }
    const formKey = token.form_key;
    const previous = surfaceKnowledge[formKey]?.level ?? "";
    setSurfaceKnowledge(formKey, "level", level); // optimistic; cursor stays put
    void setReaderSurfaceKnowledge({
      language: language(),
      item_key: token.key,
      surface_key: token.surface_key,
      level,
    }).catch(() => {
      setSurfaceKnowledge(formKey, "level", previous);
      appStore.showToast("That rating could not be saved.", "error");
    });
    enqueue({ event_type: "rate", position: token.position, value: eventValue });
  }

  function surfaceLevel(token: StoryToken): KnowledgeLevel {
    return token.form_key ? surfaceKnowledge[token.form_key]?.level ?? "" : "";
  }

  function displayLevel(token: StoryToken): KnowledgeLevel {
    return displayLevelFor(token, knowledge, surfaceKnowledge);
  }

  function markCanonical(level: "well_known" | "ignored" | "", targetKey?: string) {
    const key = targetKey ?? currentToken()?.key;
    if (!key) {
      return;
    }
    const previous = knowledge[key]?.level ?? "";
    setKnowledge(key, "level", level);
    void setWordKnowledge(key, { language: language(), level }).catch(() => {
      setKnowledge(key, "level", previous);
      appStore.showToast("That mark could not be saved.", "error");
    });
  }

  function openSentenceBreakdown() {
    const token = currentToken();
    if (!token) {
      return;
    }
    const position = token.position;
    setPopupVisible(false);
    setAnalysis({ mode: "sentence", position });
    enqueue({ event_type: "sentence_break", position });
    void ensureSentence(position);
  }

  async function playCurrentSentence() {
    const token = currentToken();
    if (!token) return;
    const span = sentenceSpans().find((candidate) =>
      token.position >= candidate.start_position && token.position < candidate.end_position);
    if (!span) {
      appStore.showToast("The current sentence could not be found.", "error");
      return;
    }
    const playbackToken = beginSpeechPlayback();
    try {
      const speech = await loadSentenceSpeech(span);
      if (playbackToken !== sentencePlaybackToken) return;
      const player = new Audio(speech.url);
      sentenceAudio = player;
      activeSentenceWords = speech.words;
      bindPlaybackState(player, playbackToken);
      player.onended = () => finishSpeechPlayback(playbackToken);
      player.onerror = () => {
        if (playbackToken === sentencePlaybackToken) {
          finishSpeechPlayback(playbackToken);
          appStore.showToast("Sentence audio could not be played.", "error");
        }
      };
      await player.play();
      animateSentencePlayback(player, speech.words, playbackToken);
    } catch {
      if (playbackToken === sentencePlaybackToken) {
        finishSpeechPlayback(playbackToken);
        appStore.showToast("Sentence audio could not be generated.", "error");
      }
    }
  }

  async function playCurrentWord(target?: StoryToken) {
    const token = target ?? currentToken();
    if (!token?.is_word) return;
    const span = sentenceSpans().find((candidate) =>
      token.position >= candidate.start_position && token.position < candidate.end_position);
    if (!span) {
      appStore.showToast("The current sentence could not be found.", "error");
      return;
    }
    const playbackToken = beginSpeechPlayback();
    try {
      const speech = await loadSentenceSpeech(span);
      if (playbackToken !== sentencePlaybackToken) return;
      const timing = speech.words.find((word) => word.position === token.position);
      if (!timing || timing.end <= timing.start) {
        finishSpeechPlayback(playbackToken);
        appStore.showToast("No sentence alignment is available for this word.", "error");
        return;
      }
      const player = new Audio(speech.url);
      player.preload = "auto";
      sentenceAudio = player;
      activeSentenceWords = [];
      bindPlaybackState(player, playbackToken);
      await waitForAudioMetadata(player);
      if (playbackToken !== sentencePlaybackToken) return;
      player.currentTime = timing.start;
      setSpeakingWord(token.position);
      player.onended = () => finishSpeechPlayback(playbackToken);
      player.onerror = () => {
        if (playbackToken === sentencePlaybackToken) {
          finishSpeechPlayback(playbackToken);
          appStore.showToast("The aligned word segment could not be played.", "error");
        }
      };
      await player.play();
      stopAtWordEnd(player, timing.end, playbackToken);
    } catch {
      if (playbackToken === sentencePlaybackToken) {
        finishSpeechPlayback(playbackToken);
        appStore.showToast("The aligned word segment could not be played.", "error");
      }
    }
  }

  function waitForAudioMetadata(player: HTMLAudioElement): Promise<void> {
    if (player.readyState >= HTMLMediaElement.HAVE_METADATA) return Promise.resolve();
    return new Promise((resolve, reject) => {
      const loaded = () => {
        player.removeEventListener("error", failed);
        resolve();
      };
      const failed = () => {
        player.removeEventListener("loadedmetadata", loaded);
        reject(new Error("audio metadata unavailable"));
      };
      player.addEventListener("loadedmetadata", loaded, { once: true });
      player.addEventListener("error", failed, { once: true });
      player.load();
    });
  }

  function stopAtWordEnd(player: HTMLAudioElement, end: number, playbackToken: number) {
    const frame = () => {
      if (playbackToken !== sentencePlaybackToken || player.paused || player.ended) return;
      if (player.currentTime >= end) {
        player.pause();
        finishSpeechPlayback(playbackToken);
        return;
      }
      sentenceAnimationFrame = requestAnimationFrame(frame);
    };
    frame();
  }

  function bindPlaybackState(player: HTMLAudioElement, playbackToken: number) {
    const update = () => {
      if (playbackToken !== sentencePlaybackToken) return;
      setAudioCurrentTime(Number.isFinite(player.currentTime) ? player.currentTime : 0);
      setAudioDuration(Number.isFinite(player.duration) ? player.duration : 0);
    };
    player.addEventListener("loadedmetadata", update);
    player.addEventListener("durationchange", update);
    player.addEventListener("timeupdate", update);
    player.addEventListener("play", () => {
      if (playbackToken === sentencePlaybackToken) setAudioPlaying(true);
    });
    player.addEventListener("pause", () => {
      if (playbackToken === sentencePlaybackToken) setAudioPlaying(false);
    });
  }

  function toggleSentencePlayback() {
    const player = sentenceAudio;
    if (!player) {
      void playCurrentSentence();
      return;
    }
    if (player.paused) {
      void player.play().then(() => {
        animateSentencePlayback(player, activeSentenceWords, sentencePlaybackToken);
      }).catch(() => appStore.showToast("Sentence audio could not be played.", "error"));
      return;
    }
    player.pause();
    clearSpeakingWord();
  }

  function seekSentencePlayback(value: number) {
    if (!sentenceAudio || !Number.isFinite(value)) return;
    sentenceAudio.currentTime = Math.min(Math.max(0, value), audioDuration());
    setAudioCurrentTime(sentenceAudio.currentTime);
  }

  async function loadSentenceSpeech(
    span: SentenceSpan,
    requireAlignment = false,
    model = readerVoiceModel(),
  ): Promise<{ url: string; words: ReaderWordTiming[] }> {
    let words: ReaderWordTiming[];
    let alignmentError: unknown;
    try {
      words = await loadSentenceAlignment(span, model);
    } catch (error) {
      // Whole-sentence playback remains usable during a transient MFA outage.
      words = [];
      alignmentError = error;
    }
    const url = await loadSentenceAudioURL(span, model);
    if (requireAlignment && (alignmentError || words.length === 0)) {
      throw alignmentError ?? new Error("empty sentence alignment");
    }
    return { url, words };
  }

  async function loadSentenceAlignment(span: SentenceSpan, model: string): Promise<ReaderWordTiming[]> {
    const cacheKey = sentenceSpeechCacheKey(span, model);
    const cached = sentenceAlignments.get(cacheKey);
    if (cached?.length) return cached;

    let request = sentenceAlignmentRequests.get(cacheKey);
    if (!request) {
      request = getStorySentenceAlignment(props.storyId, span.start_position, model)
        .then((alignment) => {
          sentenceAlignments.set(cacheKey, alignment.words);
          setAudioCacheVersion((version) => version + 1);
          return alignment.words;
        })
        .finally(() => sentenceAlignmentRequests.delete(cacheKey));
      sentenceAlignmentRequests.set(cacheKey, request);
    }
    return request;
  }

  async function loadSentenceAudioURL(span: SentenceSpan, model: string, warmServer = false): Promise<string> {
    const cacheKey = sentenceSpeechCacheKey(span, model);
    const cached = sentenceAudioURLs.get(cacheKey);
    if (cached && !warmServer) return cached;

    const requestKey = warmServer ? `${cacheKey}:warm` : cacheKey;
    let request = sentenceAudioRequests.get(requestKey);
    if (!request) {
      request = getStorySentenceAudio(props.storyId, span.start_position, model, warmServer ? "reload" : undefined)
        .then((audio) => {
          const existing = sentenceAudioURLs.get(cacheKey);
          if (existing) return existing;
          const url = URL.createObjectURL(audio);
          if (disposed) {
            URL.revokeObjectURL(url);
            throw new Error("reader disposed");
          }
          sentenceAudioURLs.set(cacheKey, url);
          setAudioCacheVersion((version) => version + 1);
          return url;
        })
        .finally(() => sentenceAudioRequests.delete(requestKey));
      sentenceAudioRequests.set(requestKey, request);
    }
    return request;
  }

  function readerVoiceModel(): string {
    return appStore.profile()?.tts_model || "default";
  }

  function sentenceSpeechCacheKey(span: SentenceSpan, model = readerVoiceModel()): string {
    return `${model}:${span.index}`;
  }

  function sentenceAudioIsReady(span: SentenceSpan, model = readerVoiceModel()): boolean {
    audioCacheVersion();
    const cacheKey = sentenceSpeechCacheKey(span, model);
    return sentenceAudioURLs.has(cacheKey) && Boolean(sentenceAlignments.get(cacheKey)?.length);
  }

  function missingSentenceAudio(): SentenceSpan[] {
    const model = readerVoiceModel();
    return sentenceSpans().filter((span) => !sentenceAudioIsReady(span, model));
  }

  function audioGenerationLabel(): string {
    const generation = audioGeneration();
    if (generation.status === "generating") {
      return `${generation.phase === "alignment" ? "MFA" : "TTS"} ${generation.completed}/${generation.total}`;
    }
    const missing = missingSentenceAudio().length;
    if (missing === 0) return "Audio ready";
    if (generation.status === "error") return `Retry audio (${missing})`;
    return `Generate audio (${missing})`;
  }

  async function generateMissingSentenceAudio() {
    if (audioGeneration().status === "generating") return;
    const model = readerVoiceModel();
    const missing = sentenceSpans().filter((span) => !sentenceAudioIsReady(span, model));
    if (missing.length === 0) return;

    const generationToken = ++audioGenerationToken;
    const ttsFailures = await runAudioGenerationPhase(
      missing,
      "tts",
      AUDIO_TTS_CONCURRENCY,
      generationToken,
      (span) => loadSentenceAudioURL(span, model, true).then(() => undefined),
    );
    if (disposed || generationToken !== audioGenerationToken) return;

    // Do not start any MFA work until every queued TTS request has settled.
    // This keeps the audio server on one model/workload at a time and ensures
    // alignment consumes the sentence audio just warmed in the server cache.
    const alignmentQueue = missing.filter((span) => !ttsFailures.has(span.index));
    const alignmentFailures = new Set<number>();
    if (alignmentQueue.length > 0) {
      setAudioGeneration({ status: "generating", phase: "alignment", completed: 0, total: alignmentQueue.length });
      try {
        const response = await alignStorySentences(
          props.storyId,
          alignmentQueue.map((span) => span.start_position),
        );
        const returned = new Set<number>();
        for (const alignment of response.alignments) {
          const span = alignmentQueue.find((candidate) => candidate.index === alignment.sentence_index);
          if (!span || alignment.words.length === 0) continue;
          sentenceAlignments.set(sentenceSpeechCacheKey(span, model), alignment.words);
          returned.add(span.index);
        }
        for (const span of alignmentQueue) {
          if (!returned.has(span.index)) alignmentFailures.add(span.index);
        }
        setAudioCacheVersion((version) => version + 1);
      } catch {
        for (const span of alignmentQueue) alignmentFailures.add(span.index);
      }
      if (generationToken === audioGenerationToken) {
        setAudioGeneration({
          status: "generating", phase: "alignment",
          completed: alignmentQueue.length, total: alignmentQueue.length,
        });
      }
    }
    if (disposed || generationToken !== audioGenerationToken) return;

    const failures = ttsFailures.size + alignmentFailures.size;
    setAudioGeneration({ status: failures ? "error" : "idle", completed: missing.length, total: missing.length });
    if (failures) {
      appStore.showToast(`${failures} sentence${failures === 1 ? "" : "s"} could not be prepared.`, "error");
    } else {
      appStore.showToast("Story audio is ready.");
    }
  }

  async function runAudioGenerationPhase(
    queue: SentenceSpan[],
    phase: "tts" | "alignment",
    concurrency: number,
    generationToken: number,
    prepare: (span: SentenceSpan) => Promise<void>,
  ): Promise<Set<number>> {
    let next = 0;
    let completed = 0;
    const failures = new Set<number>();
    setAudioGeneration({ status: "generating", phase, completed: 0, total: queue.length });

    const worker = async () => {
      while (!disposed && generationToken === audioGenerationToken) {
        const index = next++;
        if (index >= queue.length) return;
        const span = queue[index];
        try {
          await prepare(span);
        } catch {
          failures.add(span.index);
        }
        completed++;
        if (generationToken === audioGenerationToken) {
          setAudioGeneration({ status: "generating", phase, completed, total: queue.length });
        }
      }
    };

    await Promise.all(Array.from(
      { length: Math.min(concurrency, queue.length) },
      () => worker(),
    ));
    return failures;
  }

  function beginSpeechPlayback(): number {
    const playbackToken = ++sentencePlaybackToken;
    sentenceAudio?.pause();
    sentenceAudio = null;
    activeSentenceWords = [];
    setAudioPlaying(false);
    setAudioCurrentTime(0);
    setAudioDuration(0);
    if (sentenceAnimationFrame !== undefined) {
      cancelAnimationFrame(sentenceAnimationFrame);
      sentenceAnimationFrame = undefined;
    }
    clearSpeakingWord();
    return playbackToken;
  }

  function finishSpeechPlayback(playbackToken: number) {
    if (playbackToken !== sentencePlaybackToken) return;
    if (sentenceAnimationFrame !== undefined) {
      cancelAnimationFrame(sentenceAnimationFrame);
      sentenceAnimationFrame = undefined;
    }
    clearSpeakingWord();
    setAudioPlaying(false);
    sentenceAudio = null;
  }

  function animateSentencePlayback(player: HTMLAudioElement, words: ReaderWordTiming[], playbackToken: number) {
    const frame = () => {
      if (playbackToken !== sentencePlaybackToken || player.paused || player.ended) return;
      const current = words.find((word) => player.currentTime >= word.start && player.currentTime < word.end);
      if (current) setSpeakingWord(current.position);
      else clearSpeakingWord();
      sentenceAnimationFrame = requestAnimationFrame(frame);
    };
    frame();
  }

  function setSpeakingWord(position: number) {
    if (speakingPosition === position) return;
    clearSpeakingWord();
    wordEls[position]?.setAttribute("data-speaking", "");
    speakingPosition = position;
  }

  function clearSpeakingWord() {
    if (speakingPosition === undefined) return;
    wordEls[speakingPosition]?.removeAttribute("data-speaking");
    speakingPosition = undefined;
  }

  async function ensureSentence(position: number) {
    if (sentences[position] && sentences[position] !== "error") {
      return;
    }
    setSentences(position, "loading");
    try {
      setSentences(position, await sentenceBreakdown(props.storyId, { position }));
    } catch {
      setSentences(position, "error");
    }
  }

  async function ensureWord(key: string) {
    if (words[key] && words[key] !== "error") {
      return;
    }
    setWords(key, "loading");
    try {
      setWords(key, await wordBreakdown(props.storyId, { key }));
    } catch {
      setWords(key, "error");
    }
  }

  function scheduleProgressSave() {
    if (progressSaveTimer !== undefined) clearTimeout(progressSaveTimer);
    progressSaveTimer = window.setTimeout(() => {
      progressSaveTimer = undefined;
      void queueProgressSave(currentProgressPosition()).catch(() => undefined);
    }, PROGRESS_SAVE_DELAY_MS);
  }

  function currentProgressPosition(): number {
    return currentToken()?.position ?? firstSelectablePosition(tokens());
  }

  // A final browser event can run before the requestAnimationFrame that turns
  // the viewport midpoint into the active cursor. Resolve it synchronously on
  // exit so the bookmark always reflects what the learner was actually viewing.
  function leavingProgressPosition(): number {
    const midpoint = viewportMidpointCursorIndex();
    return midpoint === undefined
      ? currentProgressPosition()
      : tokens()[midpoint]?.position ?? currentProgressPosition();
  }

  function lastProgressPosition(): number {
    const list = tokens();
    const index = findSelectableToken(list, list.length - 1, -1);
    return index === undefined ? currentProgressPosition() : list[index].position;
  }

  function queueProgressSave(position: number, finished = false): Promise<void> {
    if (progressSaveTimer !== undefined) {
      clearTimeout(progressSaveTimer);
      progressSaveTimer = undefined;
    }
    const save = progressSaveChain.catch(() => undefined).then(() => saveProgressSnapshot(position, finished));
    progressSaveChain = save;
    return save;
  }

  async function saveProgressSnapshot(position: number, finished = false, keepalive = false) {
    writeSavedCursor(props.storyId, position);
    const progress = await saveReadingProgress(props.storyId, { position, finished }, { keepalive });
    if (!disposed) {
      setFinishedAt(progress.finished_at);
      if (progress.finished_at) setFinishStatus("done");
    }
  }

  async function exitReader() {
    if (exitStatus() === "saving") return;
    setExitStatus("saving");
    const finish = appStore.beginOperation();
    const results = await Promise.allSettled([
      flush(),
      queueProgressSave(leavingProgressPosition()),
    ]);
    if (results[1].status === "rejected") {
      appStore.showToast("Your position is saved on this device, but could not be synced.", "error");
    }
    finish();
    window.location.hash = routeHref("/library");
  }

  async function finishReading() {
    if (finishStatus() === "saving" || finishedAt()) return;
    setFinishStatus("saving");
    setStoryActionError("");
    const finish = appStore.beginOperation();
    try {
      await readingStart;
      await flush();
      await queueProgressSave(lastProgressPosition(), true);
      setFinishStatus("done");
      appStore.showToast("Story marked as finished.");
    } catch {
      setFinishStatus("idle");
      setStoryActionError("Your finished-reading status could not be saved.");
    } finally {
      finish();
    }
  }

  async function archiveFinishedStory() {
    if (!props.sessionId || archiveStatus() === "saving") return;
    setArchiveStatus("saving");
    setStoryActionError("");
    const finish = appStore.beginOperation();
    try {
      await queueProgressSave(leavingProgressPosition());
      await archiveSession(props.sessionId);
      window.location.hash = routeHref("/library");
    } catch {
      setStoryActionError("This story could not be archived.");
      setArchiveStatus("idle");
    } finally {
      finish();
    }
  }

  function closeOverlays() {
    if (radialMenu()) {
      setRadialMenu(null);
    } else if (analysis()) {
      setAnalysis(null);
    } else if (popupVisible()) {
      setPopupVisible(false);
    }
  }

  // ---- behavioural event batching ---------------------------------------
  function logLookup(position: number, key: string) {
    enqueue({ event_type: "lookup", position });
    setKnowledge(key, "lookup_count", (count) => (count ?? 0) + 1);
  }

  function enqueue(event: Pick<ReaderEvent, "event_type" | "position" | "value">) {
    pending.push({
      event_id: randomEventID(),
      story_id: props.storyId,
      session_id: props.sessionId,
      occurred_at: Math.floor(Date.now() / 1000),
      ...event,
    });
    scheduleFlush();
  }

  function scheduleFlush() {
    if (flushTimer !== undefined) {
      return;
    }
    flushTimer = window.setTimeout(() => {
      flushTimer = undefined;
      void flush();
    }, FLUSH_DELAY_MS);
  }

  async function flush(keepalive = false) {
    if (flushTimer !== undefined) {
      clearTimeout(flushTimer);
      flushTimer = undefined;
    }
    if (pending.length === 0) {
      return;
    }
    const batch = pending;
    pending = [];
    try {
      await postReaderEvents({ events: batch }, { keepalive });
    } catch {
      pending = batch.concat(pending); // requeue for the next flush
    }
  }

  function onVisibilityChange() {
    if (document.visibilityState === "hidden") {
      if (status() === "ready") {
        void saveProgressSnapshot(leavingProgressPosition(), false, true).catch(() => undefined);
      }
      void flush(true);
    }
  }

  function onBeforeUnload() {
    if (status() === "ready") {
      void saveProgressSnapshot(leavingProgressPosition(), false, true).catch(() => undefined);
    }
    void flush(true);
  }

  function onKeyDown(event: KeyboardEvent) {
    if (props.active === false || status() !== "ready") {
      return;
    }
    // While editing a dictionary entry every reader shortcut is inert; only
    // Escape acts (cancel edit first). Returning without preventDefault leaves
    // native typing in the edit fields untouched.
    if (storyEditorOpen()) {
      if (event.key === "Escape") {
        event.preventDefault();
        cancelStoryEdit();
      }
      return;
    }
    if (editing()) {
      if (event.key === "Escape") {
        event.preventDefault();
        cancelEdit();
      }
      return;
    }
    const target = event.target as HTMLElement | null;
    if (target?.closest("input, textarea, select")) {
      return;
    }
    switch (event.key) {
      case "ArrowRight":
      case "l":
        event.preventDefault();
        moveCursor(1);
        break;
      case "ArrowLeft":
      case "h":
        event.preventDefault();
        moveCursor(-1);
        break;
      // TODO: expose hjkl keybindings as a user preference in settings
      case "j":
        event.preventDefault();
        window.scrollBy({ top: 120, behavior: "smooth" });
        break;
      case "k":
        event.preventDefault();
        window.scrollBy({ top: -120, behavior: "smooth" });
        break;
      case " ":
      case "Spacebar":
        event.preventDefault();
        togglePopup();
        break;
      case "1":
      case "2":
      case "3":
      case "4":
      case "5":
        event.preventDefault();
        rate(event.key as KnowledgeLevel, event.key);
        break;
      case "w":
      case "W":
        event.preventDefault();
        rate("well_known", "w");
        break;
      case "i":
      case "I":
        event.preventDefault();
        rate("ignored", "i");
        break;
      case "s":
        event.preventDefault();
        void playCurrentSentence();
        break;
      case "S":
        event.preventDefault();
        void playCurrentWord();
        break;
      case "Escape":
        event.preventDefault();
        closeOverlays();
        break;
    }
  }

  function currentSentenceSpan(): SentenceSpan | undefined {
    const position = currentToken()?.position;
    return position === undefined ? undefined : sentenceSpans().find((span) =>
      position >= span.start_position && position < span.end_position);
  }

  function sentenceTextAt(position: number): string {
    const span = sentenceSpans().find((candidate) =>
      position >= candidate.start_position && position < candidate.end_position);
    if (!span) return "";
    return tokens()
      .filter((token) => token.position >= span.start_position && token.position < span.end_position)
      .map((token) => token.surface)
      .join("")
      .trim();
  }

  function tokenInCurrentSentence(position: number): boolean {
    const span = currentSentenceSpan();
    return !!span && position >= span.start_position && position < span.end_position;
  }

  function moveSentence(direction: 1 | -1) {
    const span = currentSentenceSpan();
    if (!span) return;
    const spans = sentenceSpans();
    const index = spans.findIndex((candidate) => candidate.index === span.index);
    const next = spans[index + direction];
    if (next) setReaderCursorPosition(next.start_position);
  }

  function readingProgressPercent(): number {
    const window = pageWindow();
    const globalPosition = currentToken()?.position;
    if (window && globalPosition !== undefined && window.total_tokens > 0) {
      return Math.min(100, Math.max(0, Math.round(((globalPosition + 1) / window.total_tokens) * 100)));
    }
    const selectable = tokens().filter(isSelectableToken);
    const current = currentToken()?.position;
    if (!selectable.length || current === undefined) return 0;
    const index = selectable.findIndex((token) => token.position >= current);
    return Math.round(((Math.max(0, index) + 1) / selectable.length) * 100);
  }

  function dockWordToken(key: string): StoryToken | undefined {
    const remembered = lastInspectedPosition();
    const token = remembered === undefined
      ? undefined
      : tokens().find((candidate) => candidate.position === remembered);
    return token?.key === key ? token : tokens().find((candidate) => candidate.key === key);
  }

  function bookmarkPosition() {
    void queueProgressSave(currentProgressPosition())
      .then(() => appStore.showToast("Reading position saved."))
      .catch(() => appStore.showToast("That position could not be synced.", "error"));
  }

  function openTasks() {
    if (props.onOpenTasks) {
      props.onOpenTasks();
    } else if (props.sessionId) {
      window.location.hash = sessionHref(props.sessionId, "tasks");
    }
  }

  return (
    <div
      class="reader-view"
      data-study-open={analysis() ? "" : undefined}
      data-color-knowledge={displaySettings.colorKnowledge ? "" : undefined}
      data-line-highlight={displaySettings.lineHighlight ? "" : undefined}
      data-word-highlight={displaySettings.wordHighlight ? "" : undefined}
    >
      <header class="reader-player">
        <div class="reader-player-title">
          <span class="reader-player-kicker">Reading</span>
          <strong>{props.title || "Reader"}</strong>
        </div>
        <div class="reader-player-transport" aria-label="Sentence playback">
          <button type="button" aria-label="Previous sentence" onClick={() => moveSentence(-1)}>‹</button>
          <button
            class="reader-player-play"
            type="button"
            aria-label={audioPlaying() ? "Pause sentence" : "Play sentence"}
            onClick={toggleSentencePlayback}
          >
            {audioPlaying() ? "Ⅱ" : "▶"}
          </button>
          <button type="button" aria-label="Next sentence" onClick={() => moveSentence(1)}>›</button>
          <span class="reader-player-time">{formatAudioTime(audioCurrentTime())}</span>
          <input
            type="range"
            min="0"
            max={Math.max(audioDuration(), 0.01)}
            step="0.01"
            value={audioCurrentTime()}
            aria-label="Audio position"
            disabled={!audioDuration()}
            onInput={(event) => seekSentencePlayback(Number(event.currentTarget.value))}
          />
          <span class="reader-player-time">{formatAudioTime(audioDuration())}</span>
        </div>
        <div class="reader-player-progress" aria-label={`${readingProgressPercent()}% read`}>
          <span>{readingProgressPercent()}%</span>
          <span class="reader-player-progress-track"><i style={{ width: `${readingProgressPercent()}%` }} /></span>
        </div>
        <details class="reader-more-menu">
          <summary aria-label="Reader actions">•••</summary>
          <div>
            <Show when={props.editable}>
              <button
                type="button"
                disabled={status() !== "ready" || storySaving() || storyEditLoading()}
                onClick={() => void beginStoryEdit()}
              >
                {storyEditLoading() ? "Loading full story…" : "Edit story"}
              </button>
            </Show>
            <Show when={props.canGenerateTasks}>
              <button
                type="button"
                disabled={status() !== "ready" || !taskSelection() || taskGenerating()}
                title={taskSelection() ? "Generate tasks from the selected passage" : "Select a passage first"}
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => void generateTasksForSelection()}
              >
                {taskGenerating() ? "Starting tasks…" : taskSelection() ? `Tasks from ${taskSelection()!.wordCount} words` : "Tasks from selection"}
              </button>
            </Show>
            <button
              type="button"
              disabled={status() !== "ready" || audioGeneration().status === "generating" || missingSentenceAudio().length === 0}
              onClick={() => void generateMissingSentenceAudio()}
            >
              {status() === "ready" ? audioGenerationLabel() : "Generate audio"}
            </button>
          </div>
        </details>
      </header>

      <header class="reader-topbar" aria-label="Reader">
        <button
          type="button"
          class="reader-topbar-back"
          aria-label={exitStatus() === "saving" ? "Saving" : "Exit reader"}
          disabled={exitStatus() === "saving"}
          onClick={() => void exitReader()}
        >‹</button>
        <span class="reader-topbar-title">{props.title || "Reader"}</span>
        <button
          type="button"
          class="reader-topbar-play"
          aria-label={audioPlaying() ? "Pause" : "Play sentence"}
          onClick={toggleSentencePlayback}
        >{audioPlaying() ? "Ⅱ" : "▶"}</button>
        <button
          type="button"
          class="reader-topbar-aa"
          aria-label="Display settings"
          aria-expanded={displayPanelOpen()}
          data-active={displayPanelOpen() ? "" : undefined}
          onClick={() => setDisplayPanelOpen((open) => !open)}
        >Aa</button>
        <details class="reader-topbar-more">
          <summary aria-label="More actions">•••</summary>
          <div>
            <button type="button" onClick={() => readerTextEl?.scrollIntoView({ behavior: "smooth", block: "start" })}>Back to top</button>
            <button type="button" onClick={bookmarkPosition}>Bookmark position</button>
            <Show when={props.sessionId}>
              <button type="button" onClick={openTasks}>
                Tasks{(props.pendingTasks ?? 0) > 0 ? ` (${props.pendingTasks})` : ""}
              </button>
            </Show>
            <Show when={props.editable}>
              <button
                type="button"
                disabled={status() !== "ready" || storySaving() || storyEditLoading()}
                onClick={() => void beginStoryEdit()}
              >{storyEditLoading() ? "Loading story…" : "Edit story"}</button>
            </Show>
            <button
              type="button"
              disabled={status() !== "ready" || audioGeneration().status === "generating" || missingSentenceAudio().length === 0}
              onClick={() => void generateMissingSentenceAudio()}
            >{status() === "ready" ? audioGenerationLabel() : "Generate audio"}</button>
          </div>
        </details>
        <span class="reader-topbar-progress" style={{ width: `${readingProgressPercent()}%` }} />
      </header>

      <div class="reader-shell-body">
        <nav class="reader-left-rail" aria-label="Reader navigation">
          <button type="button" onClick={() => void exitReader()} disabled={exitStatus() === "saving"}>
            <span aria-hidden="true">←</span><span>{exitStatus() === "saving" ? "Saving" : "Exit"}</span>
          </button>
          <button type="button" onClick={() => readerTextEl?.scrollIntoView({ behavior: "smooth", block: "start" })}>
            <span aria-hidden="true">☰</span><span>Contents</span>
          </button>
          <button type="button" disabled title="Search will arrive with long-form book support">
            <span aria-hidden="true">⌕</span><span>Search</span>
          </button>
          <button type="button" onClick={bookmarkPosition}>
            <span aria-hidden="true">◇</span><span>Bookmark</span>
          </button>
          <button type="button" class="reader-rail-tasks" onClick={openTasks} disabled={!props.sessionId}>
            <span aria-hidden="true">✓</span><span>Tasks</span>
            <Show when={(props.pendingTasks ?? 0) > 0}><b>{props.pendingTasks}</b></Show>
          </button>
          <button
            type="button"
            data-active={displayPanelOpen() ? "" : undefined}
            aria-expanded={displayPanelOpen()}
            onClick={() => setDisplayPanelOpen((open) => !open)}
          >
            <span class="reader-aa" aria-hidden="true">Aa</span><span>Display</span>
          </button>
        </nav>

        <main class="reader-reading-column">
          <Show when={displayPanelOpen()}>
            <aside class="reader-display-panel" aria-label="Reader display settings">
              <header><strong>Reading display</strong><button type="button" aria-label="Close display settings" onClick={() => setDisplayPanelOpen(false)}>×</button></header>
              <fieldset class="reader-flow-setting">
                <legend>Reading flow</legend>
                <label>
                  <input
                    type="radio"
                    name="reader-flow"
                    checked={displaySettings.flowMode === "pages"}
                    onChange={() => void changeReaderFlowMode("pages")}
                  /> Pages
                </label>
                <label>
                  <input
                    type="radio"
                    name="reader-flow"
                    checked={displaySettings.flowMode === "infinite"}
                    onChange={() => void changeReaderFlowMode("infinite")}
                  /> Infinite scroll
                </label>
              </fieldset>
              <label><input type="checkbox" checked={displaySettings.colorKnowledge} onChange={(event) => setDisplaySettings("colorKnowledge", event.currentTarget.checked)} /> Color words by knowledge</label>
              <label><input type="checkbox" checked={displaySettings.lineHighlight} onChange={(event) => setDisplaySettings("lineHighlight", event.currentTarget.checked)} /> Highlight current line</label>
              <label><input type="checkbox" checked={displaySettings.wordHighlight} onChange={(event) => setDisplaySettings("wordHighlight", event.currentTarget.checked)} /> Highlight current word</label>
              <label><input type="checkbox" checked={displaySettings.popupEnabled} onChange={(event) => {
                setDisplaySettings("popupEnabled", event.currentTarget.checked);
                if (!event.currentTarget.checked) setPopupVisible(false);
              }} /> Definition popup on Space</label>
              <label><input type="checkbox" checked={displaySettings.swipeAdvance} onChange={(event) => setDisplaySettings("swipeAdvance", event.currentTarget.checked)} /> Swipe to advance (touch)</label>
              <label class="reader-range-setting" data-disabled={displaySettings.swipeAdvance ? undefined : ""}>
                <span>Swipe stops at: <strong>{swipeThresholdLabel(displaySettings.swipeThreshold)}</strong></span>
                <input
                  type="range"
                  min="0"
                  max="5"
                  step="1"
                  value={displaySettings.swipeThreshold}
                  disabled={!displaySettings.swipeAdvance}
                  aria-label="Swipe knowledge threshold"
                  onInput={(event) => setDisplaySettings("swipeThreshold", Number(event.currentTarget.value))}
                />
              </label>
            </aside>
          </Show>

          <Show when={props.taskGenerationState === "generating"}>
            <p class="reader-task-generation-notice" role="status" aria-live="polite">
              Generating practice tasks in the background. You can keep reading.
            </p>
          </Show>
          <Show when={props.taskGenerationState === "failed"}>
            <p class="reader-task-generation-notice" data-tone="error" role="alert">
              Practice-task generation failed. Your story is unchanged; select a passage and try again when you want.
            </p>
          </Show>

          <Show when={storyActionError()}>
            {(message) => <p class="form-error reader-story-action-error" role="alert">{message()}</p>}
          </Show>

          <Show when={storyEditorOpen()}>
            <form class="reader-story-editor" onSubmit={(event) => { event.preventDefault(); void saveStoryEdit(); }}>
              <label for="reader-story-text">Story text</label>
              <textarea
                id="reader-story-text"
                rows={14}
                value={storyDraft()}
                disabled={storySaving()}
                ref={(element) => window.setTimeout(() => element.focus())}
                onInput={(event) => { setStoryDraft(event.currentTarget.value); setStoryActionError(""); }}
              />
              <p class="reader-story-edit-note">Saving retokenizes the story and resets this session’s reading progress, generated tasks, glossary, and prepared audio. Your learned-word history stays intact.</p>
              <div class="reader-story-edit-actions">
                <button class="primary-button" type="submit" disabled={storySaving()}>{storySaving() ? "Saving…" : "Save story"}</button>
                <button class="secondary-button" type="button" disabled={storySaving()} onClick={cancelStoryEdit}>Cancel</button>
              </div>
            </form>
          </Show>

          <Show when={!storyEditorOpen()}>
            <Switch>
              <Match when={status() === "loading"}><p class="reader-status" role="status" aria-busy="true">Loading story…</p></Match>
              <Match when={status() === "error"}>
                <div class="reader-status reader-status-error" role="alert"><p>This story could not be loaded.</p><p><a href={routeHref("/")}>Back home</a></p></div>
              </Match>
              <Match when={status() === "ready"}>
                <Show when={displaySettings.flowMode === "pages" && pageWindow() && pageWindow()!.page_count > 1
                  ? pageWindow()
                  : undefined}>
                  {(window) => (
                    <nav class="reader-page-nav" aria-label="Story pages">
                      <button
                        class="secondary-button"
                        type="button"
                        disabled={!window().has_previous || pageLoading()}
                        onClick={() => void navigatePage(-1)}
                      >Previous page</button>
                      <span>Page {window().page_index + 1} of {window().page_count}</span>
                      <button
                        class="secondary-button"
                        type="button"
                        disabled={!window().has_next || pageLoading()}
                        onClick={() => void navigatePage(1)}
                      >Next page</button>
                    </nav>
                  )}
                </Show>
                <article class="reader-paper">
                  <div
                    class="reader-text"
                    lang={language()}
                    ref={(element) => {
                      detachReaderTextGestures();
                      readerTextEl = element;
                      attachReaderTextGestures(element);
                      // Word refs are populated during the same render pass.
                      // Re-run cursor placement once that pass has settled.
                      queueMicrotask(() => {
                        if (!disposed && readerTextEl === element && element.isConnected) {
                          setReaderDOMVersion((version) => version + 1);
                        }
                      });
                    }}
                  >
                    <For each={tokens()}>
                      {(token) => (
                        <Show
                          when={token.is_word && token.key}
                          fallback={<span
                            class="reader-gap"
                            data-current-line={tokenInCurrentSentence(token.position) ? "" : undefined}
                            data-task-selected={taskTokenSelected(token.position) ? "" : undefined}
                            ref={(element) => (tokenEls[token.position] = element)}
                          >{token.surface}</span>}
                        >
                          <span
                            class="reader-word"
                            data-level={dataLevel(displayLevel(token))}
                            data-pos={token.position}
                            data-radial={radialMenu()?.position === token.position ? "" : undefined}
                            data-current-line={tokenInCurrentSentence(token.position) ? "" : undefined}
                            data-task-selected={taskTokenSelected(token.position) ? "" : undefined}
                            ref={(el) => { wordEls[token.position] = el; tokenEls[token.position] = el; }}
                            onClick={() => setReaderCursorPosition(token.position)}
                          >{token.surface}</span>
                        </Show>
                      )}
                    </For>
                  </div>
                  <Show when={displaySettings.flowMode === "infinite" ? infiniteStoryHasNext() : pageWindow()?.has_next} fallback={
                    <div class="reader-finish-panel">
                      <Show when={finishedAt()} fallback={
                        <button class="primary-button reader-finish-button" type="button" disabled={finishStatus() === "saving"} onClick={() => void finishReading()}>
                          {finishStatus() === "saving" ? "Saving…" : "Finished reading"}
                        </button>
                      }>
                        <p><strong>Finished reading</strong></p>
                        <div class="reader-finish-actions">
                          <button class="secondary-button" type="button" disabled={exitStatus() === "saving"} onClick={() => void exitReader()}>{exitStatus() === "saving" ? "Saving…" : "Exit reader"}</button>
                          <Show when={props.sessionId}><button class="secondary-button" type="button" disabled={archiveStatus() === "saving"} onClick={() => void archiveFinishedStory()}>{archiveStatus() === "saving" ? "Archiving…" : "Archive story"}</button></Show>
                        </div>
                      </Show>
                    </div>
                  }>
                    <Show when={displaySettings.flowMode === "pages"} fallback={
                      <div class="reader-infinite-sentinel" role="status" aria-live="polite">
                        {infinitePageLoading() === "next" ? "Loading more…" : "Scroll to continue"}
                      </div>
                    }>
                      <div class="reader-page-continue">
                        <button class="primary-button" type="button" disabled={pageLoading()} onClick={() => void navigatePage(1)}>
                          {pageLoading() ? "Loading page…" : "Continue to next page"}
                        </button>
                      </div>
                    </Show>
                  </Show>
                </article>
              </Match>
            </Switch>
          </Show>
        </main>

        <Show when={analysis()}>
          {(active) => (
            <aside class="reader-study-dock" aria-label={active().mode === "sentence" ? "Sentence study" : "Word study"}>
              <header class="reader-study-head">
                <span>{active().mode === "sentence" ? "Sentence" : "Word"}</span>
                <button type="button" aria-label="Close study panel" onClick={() => setAnalysis(null)}>×</button>
              </header>
              <Show when={active().mode === "word" ? active() as Extract<Analysis, { mode: "word" }> : undefined}>
                {(wordAnalysis) => {
                  const key = () => wordAnalysis().key;
                  const token = () => dockWordToken(key());
                  const note = () => loadedDefinition(key())?.grammatical_note;
                  const record = () => loadedBreakdownRecord(words[key()]);
                  return (
                    <div class="reader-word-study">
                      <div class="reader-study-word-title">
                        <h2 lang={language()}>{token()?.surface || key()}</h2>
                        <button type="button" aria-label="Play word" title="Play word" onClick={() => {
                          const inspected = token();
                          void playCurrentWord(inspected);
                        }}>▶</button>
                      </div>
                      <Show when={token()?.surface !== key()}><p class="reader-study-lemma" lang={language()}>{key()}</p></Show>
                      <Show when={note()}>{(grammar) => <p class="reader-study-grammar"><abbr title={grammar()}>{compactGrammarNote(grammar())}</abbr></p>}</Show>
                      <Show when={token()}>
                        {(ratedToken) => (
                          <div class="reader-levels reader-study-levels" role="group" aria-label="Knowledge level">
                            <For each={LEVELS}>
                              {(level) => (
                                <button
                                  type="button"
                                  class="reader-level"
                                  data-level={dataLevel(level.value)}
                                  data-active={surfaceLevel(ratedToken()) === level.value ? "" : undefined}
                                  aria-pressed={surfaceLevel(ratedToken()) === level.value}
                                  title={level.hint}
                                  onClick={() => rate(level.value, level.event, ratedToken())}
                                >{level.label.toUpperCase()}</button>
                              )}
                            </For>
                          </div>
                        )}
                      </Show>
                      <section class="reader-study-section"><h3>Definition</h3>{definitionArea(key(), true)}</section>
                      <Show when={record()} fallback={breakdownBody(words[key()], "word")}>
                        {(details) => (
                          <>
                            <details class="reader-study-disclosure">
                              <summary><span>{inflectionLabel(note())}</span><small>View</small></summary>
                              <div>{renderJSON(details().morphology ?? details().forms ?? "No inflection details returned.")}</div>
                            </details>
                            <Show when={details().root || details().morphology}>
                              <section class="reader-study-section"><h3>Word parts</h3><Show when={details().root}><p class="reader-root" lang={language()}>{String(details().root)}</p></Show><Show when={details().morphology}><div>{renderJSON(details().morphology)}</div></Show></section>
                            </Show>
                            <Show when={details().etymology}><section class="reader-study-section"><h3>Etymology</h3><div>{renderJSON(details().etymology)}</div></section></Show>
                            <Show when={details().related}><section class="reader-study-section"><h3>Related words</h3><div>{renderJSON(details().related)}</div></section></Show>
                            <Show when={details().examples}><section class="reader-study-section"><h3>Examples</h3><div>{renderJSON(details().examples)}</div></section></Show>
                          </>
                        )}
                      </Show>
                      <div class="reader-canonical-actions" role="group" aria-label="Lemma knowledge">
                        <button type="button" class="reader-deep" data-active={knowledge[key()]?.level === "well_known" ? "" : undefined} onClick={() => markCanonical("well_known", key())}>Mark lemma known</button>
                        <button type="button" class="reader-deep" data-active={knowledge[key()]?.level === "ignored" ? "" : undefined} onClick={() => markCanonical("ignored", key())}>Ignore lemma</button>
                      </div>
                      <Show when={(knowledge[key()]?.lookup_count ?? 0) > 0}><p class="reader-popup-meta">Looked up {knowledge[key()]?.lookup_count}×</p></Show>
                    </div>
                  );
                }}
              </Show>
              <Show when={active().mode === "sentence" ? active() as Extract<Analysis, { mode: "sentence" }> : undefined}>
                {(sentenceAnalysis) => (
                  <div class="reader-sentence-study">
                    <p class="reader-study-sentence" lang={language()}>{sentenceTextAt(sentenceAnalysis().position)}</p>
                    {sentenceStudyBody(sentences[sentenceAnalysis().position])}
                  </div>
                )}
              </Show>
            </aside>
          )}
        </Show>

        <nav class="reader-right-rail" aria-label="Study tools">
          <button type="button" data-active={analysis()?.mode === "word" ? "" : undefined} onClick={openRememberedWord}><span>W</span><b>Word</b></button>
          <button type="button" data-active={analysis()?.mode === "sentence" ? "" : undefined} onClick={openSentenceBreakdown}><span>S</span><b>Sentence</b></button>
        </nav>
      </div>

      <Show when={mobileLayout() && (popupVisible() || analysis() !== null || displayPanelOpen())}>
        <div class="reader-sheet-backdrop" onClick={closeMobileSheets} aria-hidden="true" />
      </Show>

      <Show when={radialMenu()}>
        {(menu) => (
          <div class="reader-radial" role="dialog" aria-label={`Rate "${menu().surface}"`}>
            <div class="reader-radial-scrim" onClick={closeRadialMenu} aria-hidden="true" />
            <span
              class="reader-radial-word"
              lang={language()}
              data-level={menu().level}
              style={{ left: `${menu().pressX}px`, top: `${menu().pressY}px` }}
            >{menu().surface}</span>
            <div class="reader-radial-ring" style={{ left: `${menu().cx}px`, top: `${menu().cy}px` }}>
              <For each={menu().buttons}>
                {(button) => (
                  <button
                    type="button"
                    class="reader-radial-btn"
                    data-level={button.level}
                    data-active={surfaceLevelAt(menu().position) === button.value ? "" : undefined}
                    data-hot={radialHot() === button.event ? "" : undefined}
                    style={{ transform: `translate(-50%, -50%) translate(${button.x}px, ${button.y}px)` }}
                    aria-label={`${button.label} — ${button.hint}`}
                    onClick={() => rateFromRadial(button.value, button.event)}
                  ><span class="reader-radial-dot">{button.label.toUpperCase()}</span></button>
                )}
              </For>
            </div>
            <div
              class="reader-radial-bubble"
              data-side={menu().side}
              style={{ left: `${menu().bubbleX}px`, top: `${menu().bubbleY}px` }}
            >
              <div class="reader-radial-bubble-head">
                <b lang={language()}>{menu().surface}</b>
                <button
                  type="button"
                  aria-label="Play word"
                  onClick={() => void playCurrentWord(tokens().find((candidate) => candidate.position === menu().position))}
                >▶</button>
              </div>
              <p class="reader-radial-gloss">{radialGloss()}</p>
              <button type="button" class="reader-radial-more" onClick={openWordStudyFromRadial}>Breakdown ▸</button>
            </div>
          </div>
        )}
      </Show>

      <Show when={popupVisible() && displaySettings.popupEnabled ? currentToken() : undefined}>
        {(token) => (
          <Show when={token().key}>
            {(key) => (
              <aside class="reader-popup" style={popupStyle()} role="dialog" aria-label="Word definition" ref={(el) => (popupEl = el)}>
                <button class="reader-popup-close" type="button" aria-label="Close" onClick={() => setPopupVisible(false)}>×</button>
                <p class="reader-popup-surface" lang={language()}>{token().surface}</p>
                <Show when={token().surface !== key()}><p class="reader-popup-key" lang={language()}>{key()}</p></Show>
                {definitionArea(key())}
                <button class="reader-popup-study-link" type="button" onClick={inspectCurrentWord}>Open in Word panel</button>
              </aside>
            )}
          </Show>
        )}
      </Show>
    </div>
  );
}

type SavedCursor = { position: number; savedAt: number };

function readSavedCursor(storyId: string): SavedCursor | undefined {
  try {
    const raw = localStorage.getItem(readerPositionKey(storyId));
    if (raw === null) {
      return undefined;
    }
    if (/^\d+$/.test(raw)) {
      const position = Number(raw);
      return Number.isSafeInteger(position) ? { position, savedAt: Date.now() / 1000 } : undefined;
    }
    const value = JSON.parse(raw) as Partial<SavedCursor>;
    return Number.isSafeInteger(value.position) && Number.isFinite(value.savedAt)
      ? { position: value.position!, savedAt: value.savedAt! }
      : undefined;
  } catch {
    return undefined;
  }
}

function writeSavedCursor(storyId: string, position: number) {
  try {
    localStorage.setItem(readerPositionKey(storyId), JSON.stringify({ position, savedAt: Date.now() / 1000 } satisfies SavedCursor));
  } catch {
    // Reading should continue even when localStorage is unavailable.
  }
}

function clearSavedCursor(storyId: string) {
  try {
    localStorage.removeItem(readerPositionKey(storyId));
  } catch {
    // The server-side edit still reset progress even if localStorage is unavailable.
  }
}

function readerStoryActionMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
}

function readerPositionKey(storyId: string): string {
  return `${READER_POSITION_PREFIX}${storyId}`;
}

function resolveCursorIndex(list: StoryToken[], position: number | undefined): number {
  const first = findSelectableToken(list, 0, 1);
  if (first === undefined) {
    return 0;
  }
  if (position === undefined) {
    return first;
  }
  const next = list.findIndex((token) => token.position >= position);
  const target = next >= 0 ? next : list.length - 1;
  if (isSelectableToken(list[target])) {
    return target;
  }
  return findSelectableToken(list, target + 1, 1) ?? findSelectableToken(list, target - 1, -1) ?? first;
}

function firstSelectablePosition(list: StoryToken[]): number {
  const index = findSelectableToken(list, 0, 1);
  return index === undefined ? 0 : list[index].position;
}

function lastSelectablePosition(list: StoryToken[]): number {
  const index = findSelectableToken(list, list.length - 1, -1);
  return index === undefined ? firstSelectablePosition(list) : list[index].position;
}

function findSelectableToken(list: StoryToken[], start: number, direction: 1 | -1): number | undefined {
  for (let i = start; i >= 0 && i < list.length; i += direction) {
    if (isSelectableToken(list[i])) {
      return i;
    }
  }
  return undefined;
}

function isSelectableToken(token: StoryToken | undefined): boolean {
  return Boolean(token?.is_word && token.key);
}

function definitionBody(entry: Loadable<Definition> | undefined, lang: string, showGrammar = true): JSX.Element {
  if (!entry || entry === "loading") {
    return <p class="reader-popup-loading">Looking up…</p>;
  }
  if (entry === "error") {
    return <p class="reader-popup-error">No definition available.</p>;
  }
  return (
    <>
      <p class="reader-gloss">{entry.gloss}</p>
      <Show when={showGrammar && entry.grammatical_note}>
        <p class="reader-grammar">{entry.grammatical_note}</p>
      </Show>
      <Show when={entry.example}>
        <p class="reader-example" lang={lang}>{entry.example}</p>
      </Show>
      <Show when={entry.notes}>
        {(notes) => <p class="reader-notes">{notes()}</p>}
      </Show>
      <Show when={entry.source !== "user"}>
        <span class="reader-source" data-source={entry.source}>{entry.source}</span>
      </Show>
    </>
  );
}

function readReaderDisplaySettings(): ReaderDisplaySettings {
  const defaults: ReaderDisplaySettings = {
    colorKnowledge: true,
    lineHighlight: false,
    wordHighlight: true,
    popupEnabled: true,
    flowMode: "pages",
    swipeAdvance: true,
    swipeThreshold: 3,
  };
  try {
    const stored = JSON.parse(localStorage.getItem(READER_DISPLAY_SETTINGS_KEY) ?? "null") as Partial<ReaderDisplaySettings> | null;
    if (!stored) return defaults;
    const threshold = Number(stored.swipeThreshold);
    return {
      colorKnowledge: typeof stored.colorKnowledge === "boolean" ? stored.colorKnowledge : defaults.colorKnowledge,
      lineHighlight: typeof stored.lineHighlight === "boolean" ? stored.lineHighlight : defaults.lineHighlight,
      wordHighlight: typeof stored.wordHighlight === "boolean" ? stored.wordHighlight : defaults.wordHighlight,
      popupEnabled: typeof stored.popupEnabled === "boolean" ? stored.popupEnabled : defaults.popupEnabled,
      flowMode: stored.flowMode === "infinite" || stored.flowMode === "pages" ? stored.flowMode : defaults.flowMode,
      swipeAdvance: typeof stored.swipeAdvance === "boolean" ? stored.swipeAdvance : defaults.swipeAdvance,
      swipeThreshold: Number.isFinite(threshold) ? Math.min(5, Math.max(0, Math.round(threshold))) : defaults.swipeThreshold,
    };
  } catch {
    return defaults;
  }
}

function writeReaderDisplaySettings(settings: ReaderDisplaySettings) {
  try {
    localStorage.setItem(READER_DISPLAY_SETTINGS_KEY, JSON.stringify(settings));
  } catch {
    // Display preferences are optional; reading remains usable without storage.
  }
}

function formatAudioTime(seconds: number): string {
  const safe = Number.isFinite(seconds) ? Math.max(0, Math.floor(seconds)) : 0;
  return `${Math.floor(safe / 60)}:${String(safe % 60).padStart(2, "0")}`;
}

function compactGrammarNote(note: string): string {
  const replacements: [RegExp, string][] = [
    [/\bnoun\b/gi, "n."],
    [/\bverb\b/gi, "v."],
    [/\badjective\b/gi, "adj."],
    [/\badverb\b/gi, "adv."],
    [/\bpronoun\b/gi, "pron."],
    [/\bpreposition\b/gi, "prep."],
    [/\bconjunction\b/gi, "conj."],
    [/\bmasculine\b/gi, "masc."],
    [/\bfeminine\b/gi, "fem."],
    [/\bneuter\b/gi, "neut."],
    [/\bsingular\b/gi, "sg."],
    [/\bplural\b/gi, "pl."],
    [/\bnominative\b/gi, "nom."],
    [/\bgenitive\b/gi, "gen."],
    [/\baccusative\b/gi, "acc."],
    [/\bvocative\b/gi, "voc."],
    [/\bindicative\b/gi, "ind."],
    [/\bsubjunctive\b/gi, "subj."],
    [/\bimperative\b/gi, "imp."],
    [/\bpresent\b/gi, "pres."],
    [/\bimperfect\b/gi, "impf."],
    [/\baorist\b/gi, "aor."],
    [/\bactive\b/gi, "act."],
    [/\bpassive\b/gi, "pass."],
  ];
  return replacements.reduce((value, [pattern, replacement]) => value.replace(pattern, replacement), note)
    .replace(/\s*[;,]\s*/g, " · ");
}

function inflectionLabel(note: string | undefined): string {
  if (/\bverb\b|\bv\./i.test(note ?? "")) return "Conjugation";
  if (/\bnoun\b|\badjective\b|\bpronoun\b|\bn\.|\badj\.|\bpron\./i.test(note ?? "")) return "Declension";
  return "Forms";
}

function loadedBreakdownRecord(entry: Loadable<unknown> | undefined): Record<string, unknown> | undefined {
  return entry && entry !== "loading" && entry !== "error" ? asRecord(entry) : undefined;
}

function sentenceStudyBody(entry: Loadable<unknown> | undefined): JSX.Element {
  if (!entry || entry === "loading") {
    return <p class="reader-breakdown-loading" aria-busy="true">Analyzing…</p>;
  }
  if (entry === "error") {
    return <p class="reader-breakdown-error">This sentence analysis is unavailable right now.</p>;
  }
  const record = asRecord(entry);
  const graph = syntaxGraphFromBreakdown(entry);
  return (
    <>
      <section class="reader-study-section">
        <h3>Translation</h3>
        <p>{stringValue(record?.translation) ?? "No translation returned."}</p>
      </section>
      <Show when={graph}>{(syntax) => <SyntaxGraphView graph={syntax()} />}</Show>
      <Show when={Array.isArray(record?.phrases) && record!.phrases.length > 0}>
        <section class="reader-study-section"><h3>Phrases & patterns</h3><div>{renderJSON(record!.phrases)}</div></section>
      </Show>
      <Show when={Array.isArray(record?.grammar) && record!.grammar.length > 0}>
        <details class="reader-study-disclosure"><summary><span>Grammar notes</span><small>View</small></summary><div>{renderJSON(record!.grammar)}</div></details>
      </Show>
      <details class="reader-breakdown-details"><summary>All sentence details</summary><div class="reader-json">{renderJSON(entry)}</div></details>
    </>
  );
}

function definitionSourcePicker(
  entry: Loadable<DefinitionOption[]> | undefined,
  current: Definition | null,
  choose: (option: DefinitionOption) => void,
): JSX.Element {
  if (!entry || entry === "loading") {
    return <p class="reader-source-picker-status">Loading sources…</p>;
  }
  if (entry === "error") {
    return <p class="reader-source-picker-status reader-popup-error">Sources could not be loaded.</p>;
  }
  if (entry.length === 0) {
    return <p class="reader-source-picker-status">No other stored sources.</p>;
  }
  return (
    <div class="reader-source-options" aria-label="Available definition sources">
      <For each={entry}>
        {(option) => {
          const selected = () => current?.source === option.source && current.key === option.key && current.gloss === option.gloss;
          return (
            <button
              class="reader-source-option"
              type="button"
              data-selected={selected() ? "" : undefined}
              aria-pressed={selected()}
              onClick={() => choose(option)}
            >
              <span class="reader-source-option-head">
                <span>{definitionSourceLabel(option.source)}</span>
                <Show when={option.key !== current?.trace.query_key}><span lang="el">{option.key}</span></Show>
              </span>
              <span>{option.gloss}</span>
              <Show when={option.grammatical_note}>
                {(note) => <small>{note()}</small>}
              </Show>
            </button>
          );
        }}
      </For>
    </div>
  );
}

function definitionSourceLabel(source: DefinitionOption["source"]): string {
  switch (source) {
    case "wiktionary": return "English Wiktionary";
    case "wiktionary-native": return "Greek Wiktionary";
    case "wiktionary-translated": return "Translated Wiktionary";
    case "glossary": return "Story glossary";
    case "metadata": return "Learning metadata";
    case "llm": return "AI definition";
    case "user": return "Your definition";
  }
}

function breakdownBody(entry: Loadable<unknown> | undefined, mode: Exclude<Analysis, null>["mode"]): JSX.Element {
  if (!entry || entry === "loading") {
    return <p class="reader-breakdown-loading" aria-busy="true">Analyzing…</p>;
  }
  if (entry === "error") {
    return <p class="reader-breakdown-error">This breakdown is unavailable right now.</p>;
  }
  const graph = mode === "sentence" ? syntaxGraphFromBreakdown(entry) : null;
  if (graph) {
    return (
      <div class="reader-sentence-breakdown">
        <SyntaxGraphView graph={graph} />
        <details class="reader-breakdown-details">
          <summary>Flat details</summary>
          <div class="reader-json">{renderJSON(entry)}</div>
        </details>
      </div>
    );
  }
  return <div class="reader-json">{renderJSON(entry)}</div>;
}

function SyntaxGraphView(props: { graph: SyntaxGraph }): JSX.Element {
  const forest = buildSyntaxForest(props.graph);
  return (
    <section class="reader-syntax" aria-label="Syntax tree">
      <div class="reader-syntax-head">
        <h3>Syntax tree</h3>
        <Show when={props.graph.version}>
          {(version) => <span>{version()}</span>}
        </Show>
      </div>
      <div class="reader-syntax-scroll">
        <ol class="reader-syntax-tree">
          <For each={forest}>{(branch) => <SyntaxTreeItem branch={branch} />}</For>
        </ol>
      </div>
    </section>
  );
}

function SyntaxTreeItem(props: { branch: SyntaxTreeBranch }): JSX.Element {
  const node = () => props.branch.node;
  return (
    <li class="reader-syntax-item">
      <article class="reader-syntax-node" data-kind={node().kind}>
        <div class="reader-syntax-meta">
          <span class="reader-syntax-kind">{humanize(node().kind)}</span>
          <Show when={props.branch.relation}>
            {(relation) => <span class="reader-syntax-relation">{humanize(relation())}</span>}
          </Show>
        </div>
        <div class="reader-syntax-copy">
          <strong>{syntaxNodeTitle(node())}</strong>
          <Show when={node().surface}>
            {(surface) => <span class="reader-syntax-surface">{surface()}</span>}
          </Show>
          <Show when={node().gloss}>
            {(gloss) => <span class="reader-syntax-gloss">{gloss()}</span>}
          </Show>
        </div>
        <Show when={formatSpan(node())}>
          {(span) => <span class="reader-syntax-span">{span()}</span>}
        </Show>
      </article>
      <Show when={props.branch.children.length > 0}>
        <ol class="reader-syntax-children">
          <For each={props.branch.children}>{(child) => <SyntaxTreeItem branch={child} />}</For>
        </ol>
      </Show>
    </li>
  );
}

function syntaxGraphFromBreakdown(value: unknown): SyntaxGraph | null {
  const root = asRecord(value);
  const rawGraph = asRecord(root?.syntax_graph);
  const rawNodes = rawGraph?.nodes;
  if (!Array.isArray(rawNodes)) {
    return null;
  }
  const nodes: SyntaxNode[] = [];
  for (const rawNode of rawNodes) {
    const node = asRecord(rawNode);
    const id = stringValue(node?.id);
    if (!id) {
      continue;
    }
    nodes.push({
      id,
      kind: stringValue(node?.kind) ?? "node",
      label: stringValue(node?.label),
      surface: stringValue(node?.surface),
      gloss: stringValue(node?.gloss),
      itemKey: stringValue(node?.item_key),
      spanStart: numberValue(node?.span_start),
      spanEnd: numberValue(node?.span_end),
    });
  }
  if (nodes.length === 0) {
    return null;
  }

  const edges: SyntaxEdge[] = [];
  if (Array.isArray(rawGraph?.edges)) {
    for (const rawEdge of rawGraph.edges) {
      const edge = asRecord(rawEdge);
      const source = stringValue(edge?.source);
      const target = stringValue(edge?.target);
      if (!source || !target) {
        continue;
      }
      edges.push({
        source,
        target,
        relation: stringValue(edge?.relation),
        label: stringValue(edge?.label),
      });
    }
  }

  const roots = Array.isArray(rawGraph?.roots) ? rawGraph.roots.map(stringValue).filter(isString) : [];
  return {
    version: stringValue(rawGraph?.version),
    roots,
    nodes,
    edges,
  };
}

function buildSyntaxForest(graph: SyntaxGraph): SyntaxTreeBranch[] {
  const nodesByID = new Map(graph.nodes.map((node) => [node.id, node]));
  const childrenBySource = new Map<string, SyntaxEdge[]>();
  const targeted = new Set<string>();
  for (const edge of graph.edges) {
    if (!nodesByID.has(edge.source) || !nodesByID.has(edge.target) || edge.source === edge.target) {
      continue;
    }
    targeted.add(edge.target);
    const children = childrenBySource.get(edge.source) ?? [];
    children.push(edge);
    childrenBySource.set(edge.source, children);
  }
  for (const children of childrenBySource.values()) {
    children.sort((a, b) => compareSyntaxNodes(nodesByID.get(a.target), nodesByID.get(b.target)));
  }

  let rootIDs = graph.roots.filter((id) => nodesByID.has(id));
  if (rootIDs.length === 0) {
    rootIDs = graph.nodes.filter((node) => !targeted.has(node.id)).sort(compareSyntaxNodes).map((node) => node.id);
  }
  if (rootIDs.length === 0) {
    rootIDs = [...graph.nodes].sort(compareSyntaxNodes).map((node) => node.id);
  }

  return rootIDs.map((id) => buildSyntaxBranch(id, nodesByID, childrenBySource, new Set()));
}

function buildSyntaxBranch(
  id: string,
  nodesByID: Map<string, SyntaxNode>,
  childrenBySource: Map<string, SyntaxEdge[]>,
  path: Set<string>,
  relation?: string,
): SyntaxTreeBranch {
  const node = nodesByID.get(id);
  if (!node) {
    throw new Error(`missing syntax node ${id}`);
  }
  const nextPath = new Set(path);
  nextPath.add(id);
  const children = (childrenBySource.get(id) ?? [])
    .filter((edge) => !nextPath.has(edge.target))
    .map((edge) => buildSyntaxBranch(edge.target, nodesByID, childrenBySource, nextPath, edge.label || edge.relation));
  return { node, relation, children };
}

function compareSyntaxNodes(a: SyntaxNode | undefined, b: SyntaxNode | undefined): number {
  if (!a || !b) {
    return a ? -1 : b ? 1 : 0;
  }
  const aStart = a.spanStart ?? Number.MAX_SAFE_INTEGER;
  const bStart = b.spanStart ?? Number.MAX_SAFE_INTEGER;
  if (aStart !== bStart) {
    return aStart - bStart;
  }
  const aEnd = a.spanEnd ?? aStart;
  const bEnd = b.spanEnd ?? bStart;
  if (aEnd !== bEnd) {
    return bEnd - aEnd;
  }
  return a.id.localeCompare(b.id);
}

function syntaxNodeTitle(node: SyntaxNode): string {
  return node.label || node.surface || node.itemKey || node.id;
}

function formatSpan(node: SyntaxNode): string | undefined {
  if (node.spanStart === undefined || node.spanEnd === undefined) {
    return undefined;
  }
  return `${node.spanStart}-${node.spanEnd}`;
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}

function stringValue(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value : undefined;
}

function numberValue(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function isString(value: string | undefined): value is string {
  return value !== undefined;
}

// Renders whatever JSON the prompt builder returns: objects as labelled rows,
// arrays as lists, scalars as text. Robust to the breakdown shape changing.
function renderJSON(value: unknown): JSX.Element {
  if (value === null || value === undefined || value === "") {
    return <span class="reader-json-empty">—</span>;
  }
  if (Array.isArray(value)) {
    return (
      <ul class="reader-json-list">
        <For each={value}>{(item) => <li>{renderJSON(item)}</li>}</For>
      </ul>
    );
  }
  if (typeof value === "object") {
    return (
      <dl class="reader-json-object">
        <For each={Object.entries(value as Record<string, unknown>)}>
          {([fieldKey, fieldValue]) => (
            <>
              <dt>{humanize(fieldKey)}</dt>
              <dd>{renderJSON(fieldValue)}</dd>
            </>
          )}
        </For>
      </dl>
    );
  }
  return <span class="reader-json-scalar">{String(value)}</span>;
}

function humanize(key: string): string {
  return key.replace(/[_-]+/g, " ").replace(/\b\w/g, (char) => char.toUpperCase());
}

function displayLevelFor(
  token: StoryToken,
  canonical: Record<string, ReaderKnowledge>,
  forms: Record<string, ReaderSurfaceKnowledge>,
): KnowledgeLevel {
  const canonicalLevel = token.key ? canonical[token.key]?.level : "";
  if (canonicalLevel === "well_known" || canonicalLevel === "ignored") {
    return canonicalLevel;
  }
  return token.form_key ? forms[token.form_key]?.level ?? "" : "";
}

// Maps the API knowledge level onto the CSS data-level vocabulary used by the
// theme custom properties ("" → unseen, well_known → well-known).
function dataLevel(level: KnowledgeLevel | undefined): string {
  switch (level) {
    case "1":
    case "2":
    case "3":
    case "4":
    case "5":
      return level;
    case "well_known":
      return "well-known";
    case "ignored":
      return "ignored";
    default:
      return "unseen";
  }
}

function randomEventID(): string | undefined {
  return typeof crypto !== "undefined" && "randomUUID" in crypto ? crypto.randomUUID() : undefined;
}
