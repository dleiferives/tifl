import { createEffect, createSignal, For, Match, onCleanup, onMount, Show, Switch } from "solid-js";
import type { JSX } from "solid-js";
import { createStore } from "solid-js/store";
import {
  completeSession,
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
  startReading,
  setWordKnowledge,
  wordBreakdown,
} from "../api";
import type { APISchema } from "../api";
import { routeHref } from "../router";
import { appStore } from "../store";

type StoryToken = APISchema<"StoryToken">;
type StoryLoad = APISchema<"StoryLoad">;
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
type Analysis = { mode: "sentence"; position: number } | { mode: "word"; key: string } | null;
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
const READER_POSITION_PREFIX = "tifl.reader.position.";

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

export function ReaderView(props: {
  storyId: string;
  sessionId?: string;
  story?: StoryLoad | null;
  active?: boolean;
  onReadingStarted?: () => void;
  onSessionComplete?: () => void;
}) {
  const [status, setStatus] = createSignal<"loading" | "ready" | "error">("loading");
  const [completeStatus, setCompleteStatus] = createSignal<"idle" | "saving" | "done">("idle");
  const [tokens, setTokens] = createSignal<StoryToken[]>([]);
  const [sentenceSpans, setSentenceSpans] = createSignal<SentenceSpan[]>([]);
  const [language, setLanguage] = createSignal("");
  const [knowledge, setKnowledge] = createStore<Record<string, ReaderKnowledge>>({});
  const [surfaceKnowledge, setSurfaceKnowledge] = createStore<Record<string, ReaderSurfaceKnowledge>>({});
  const [cursor, setCursor] = createSignal(0);
  const [popupVisible, setPopupVisible] = createSignal(false);
  const [popupPos, setPopupPos] = createSignal<{ top: number; left: number } | null>(null);
  const [definitions, setDefinitions] = createStore<Record<string, Loadable<Definition>>>({});
  const [definitionOptions, setDefinitionOptions] = createStore<Record<string, Loadable<DefinitionOption[]>>>({});
  const [sourcePickerKey, setSourcePickerKey] = createSignal("");
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
  const [sentences, setSentences] = createStore<Record<number, Loadable<unknown>>>({});
  const [words, setWords] = createStore<Record<string, Loadable<unknown>>>({});

  // One element ref per word token, indexed by token position. Used to move the
  // cursor highlight surgically (two DOM writes) and to anchor the popup.
  const wordEls: (HTMLElement | undefined)[] = [];
  // The popup element, so reposition can measure its (edit-mode-variable) height.
  let popupEl: HTMLElement | undefined;
  let pending: ReaderEvent[] = [];
  let flushTimer: number | undefined;
  let readingStart: Promise<void> | null = null;
  let dictionaryMutationSeq = 0;
  const dictionaryMutationIds: Record<string, number> = {};
  const sentenceAudioURLs = new Map<string, string>();
  const sentenceAudioRequests = new Map<string, Promise<{ url: string; words: ReaderWordTiming[] }>>();
  const sentenceAlignments = new Map<string, ReaderWordTiming[]>();
  let sentenceAudio: HTMLAudioElement | null = null;
  let sentencePlaybackToken = 0;
  let sentenceAnimationFrame: number | undefined;
  let speakingPosition: number | undefined;

  const currentToken = () => tokens()[cursor()];

  onMount(() => {
    document.addEventListener("keydown", onKeyDown);
    document.addEventListener("visibilitychange", onVisibilityChange);
    window.addEventListener("beforeunload", onBeforeUnload);
    window.addEventListener("resize", repositionPopup);
    window.addEventListener("scroll", repositionPopup, true);
    void load();
  });

  onCleanup(() => {
    document.removeEventListener("keydown", onKeyDown);
    document.removeEventListener("visibilitychange", onVisibilityChange);
    window.removeEventListener("beforeunload", onBeforeUnload);
    window.removeEventListener("resize", repositionPopup);
    window.removeEventListener("scroll", repositionPopup, true);
    sentencePlaybackToken++;
    sentenceAudio?.pause();
    if (sentenceAnimationFrame !== undefined) cancelAnimationFrame(sentenceAnimationFrame);
    clearSpeakingWord();
    for (const objectURL of sentenceAudioURLs.values()) URL.revokeObjectURL(objectURL);
    sentenceAudioURLs.clear();
    sentenceAudioRequests.clear();
    sentenceAlignments.clear();
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
      const story = props.story?.story_id === props.storyId ? props.story : await getStory(props.storyId);
      applyStory(story);
      setStatus("ready");
    } catch {
      setStatus("error");
    }
  }

  function applyStory(story: StoryLoad) {
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
    setSentenceSpans(story.sentences);
    setLanguage(story.language);
    setCursor(resolveCursorIndex(story.tokens, readSavedCursor(props.storyId)));
  }

  // Cursor highlight: clear the previous word, mark the new one. Two writes.
  let prevCursor: number | undefined;
  createEffect(() => {
    const c = cursor();
    if (status() !== "ready") {
      return;
    }
    const token = tokens()[c];
    if (token) {
      writeSavedCursor(props.storyId, token.position);
    }
    if (prevCursor !== undefined && prevCursor !== c) {
      wordElementForCursor(prevCursor)?.removeAttribute("data-cursor");
    }
    const el = wordElementForCursor(c);
    if (el) {
      el.setAttribute("data-cursor", "");
      el.scrollIntoView({ block: "nearest", inline: "nearest" });
    }
    prevCursor = c;
  });

  // While the popup is open it follows the cursor: each word it reveals is a
  // lookup (the strongest acquisition signal), so it gets logged and refetched.
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
    logLookup(token.position, token.key);
    void ensureDefinition(token.key);
    repositionPopup();
  });

  function moveCursor(direction: 1 | -1) {
    if (editing()) {
      return; // edit mode pins the popup to its word
    }
    const list = tokens();
    for (let i = cursor() + direction; i >= 0 && i < list.length; i += direction) {
      if (list[i].is_word && list[i].key) {
        setCursor(i);
        return;
      }
    }
  }

  function setReaderCursorPosition(position: number) {
    if (editing()) {
      return; // edit mode pins the popup to its word
    }
    setCursor(resolveCursorIndex(tokens(), position));
  }

  function wordElementForCursor(index: number): HTMLElement | undefined {
    const token = tokens()[index];
    return token ? wordEls[token.position] : undefined;
  }

  function togglePopup() {
    if (!currentToken()?.key) {
      return;
    }
    setPopupVisible((visible) => !visible);
  }

  function repositionPopup() {
    if (!popupVisible()) {
      return;
    }
    const el = wordElementForCursor(cursor());
    if (!el) {
      setPopupPos(null);
      return;
    }
    const rect = el.getBoundingClientRect();
    const popupWidth = popupEl?.offsetWidth ?? 320;
    const left = Math.max(8, Math.min(rect.left, window.innerWidth - 8 - popupWidth));
    // Prefer just below the word; if the popup (taller in edit mode) would spill
    // past the viewport bottom, flip it above the word, then clamp as a last resort.
    const height = popupEl?.offsetHeight ?? 0;
    let top = rect.bottom + 8;
    if (height > 0 && top + height > window.innerHeight - 8) {
      const above = rect.top - 8 - height;
      top = above >= 8 ? above : Math.max(8, window.innerHeight - 8 - height);
    }
    setPopupPos({ top, left });
  }

  const popupStyle = (): JSX.CSSProperties => {
    const pos = popupPos();
    if (!pos) {
      return { visibility: "hidden" };
    }
    return { top: `${pos.top}px`, left: `${pos.left}px` };
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

  function definitionArea(key: string): JSX.Element {
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
        {definitionBody(entry, language())}
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

  function rate(level: KnowledgeLevel, eventValue: string) {
    const token = currentToken();
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

  function markCanonical(level: "well_known" | "ignored" | "") {
    const token = currentToken();
    if (!token?.key) {
      return;
    }
    const key = token.key;
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

  async function playCurrentWord() {
    const token = currentToken();
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

  async function loadSentenceSpeech(span: SentenceSpan): Promise<{ url: string; words: ReaderWordTiming[] }> {
    const model = appStore.profile()?.tts_model || "default";
    const cacheKey = `${model}:${span.index}`;
    const cachedURL = sentenceAudioURLs.get(cacheKey);
    if (cachedURL) return { url: cachedURL, words: sentenceAlignments.get(cacheKey) ?? [] };

    let request = sentenceAudioRequests.get(cacheKey);
    if (!request) {
      request = (async () => {
        let words: ReaderWordTiming[] = [];
        try {
          const alignment = await getStorySentenceAlignment(props.storyId, span.start_position, model);
          words = alignment.words;
        } catch {
          // Alignment is an enhancement: audio remains usable if MFA is down.
        }
        const audio = await getStorySentenceAudio(props.storyId, span.start_position, model);
        const url = URL.createObjectURL(audio);
        sentenceAudioURLs.set(cacheKey, url);
        sentenceAlignments.set(cacheKey, words);
        return { url, words };
      })().finally(() => sentenceAudioRequests.delete(cacheKey));
      sentenceAudioRequests.set(cacheKey, request);
    }
    return request;
  }

  function beginSpeechPlayback(): number {
    const playbackToken = ++sentencePlaybackToken;
    sentenceAudio?.pause();
    sentenceAudio = null;
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

  function openWordBreakdown(key: string) {
    setAnalysis({ mode: "word", key });
    void ensureWord(key);
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

  function breakdownEntry(active: Exclude<Analysis, null>): Loadable<unknown> | undefined {
    return active.mode === "sentence" ? sentences[active.position] : words[active.key];
  }

  async function completeCurrentSession() {
    if (!props.sessionId || completeStatus() === "saving") {
      return;
    }
    setCompleteStatus("saving");
    const finish = appStore.beginOperation();
    try {
      await readingStart;
      await flush();
      await completeSession(props.sessionId);
      setCompleteStatus("done");
      appStore.showToast("Session marked complete.");
      props.onSessionComplete?.();
    } catch {
      setCompleteStatus("idle");
      appStore.showToast("This session could not be completed.", "error");
    } finally {
      finish();
    }
  }

  function closeOverlays() {
    if (analysis()) {
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
      void flush(true);
    }
  }

  function onBeforeUnload() {
    void flush(true);
  }

  function onKeyDown(event: KeyboardEvent) {
    if (props.active === false || status() !== "ready") {
      return;
    }
    // While editing a dictionary entry every reader shortcut is inert; only
    // Escape acts (cancel edit first). Returning without preventDefault leaves
    // native typing in the edit fields untouched.
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

  return (
    <section class="reader-view">
      <header class="reader-toolbar">
        <h1>Reader</h1>
        <p class="reader-hints" aria-label="Keyboard shortcuts">
          <span><kbd>←</kbd><kbd>→</kbd> / <kbd>h</kbd><kbd>l</kbd> move · <kbd>j</kbd><kbd>k</kbd> scroll</span>
          <span><kbd>Space</kbd> define</span>
          <span><kbd>1</kbd>–<kbd>5</kbd> rate</span>
          <span><kbd>w</kbd> known</span>
          <span><kbd>i</kbd> ignore</span>
          <span><kbd>s</kbd> sentence · <kbd>Shift</kbd>+<kbd>s</kbd> word</span>
        </p>
        <Show when={props.sessionId}>
          <button
            class="primary-button reader-complete-button"
            type="button"
            disabled={status() !== "ready" || completeStatus() === "saving" || completeStatus() === "done"}
            onClick={() => void completeCurrentSession()}
          >
            {completeStatus() === "done" ? "Completed" : completeStatus() === "saving" ? "Completing..." : "Complete session"}
          </button>
        </Show>
      </header>

      <Switch>
        <Match when={status() === "loading"}>
          <p class="reader-status" role="status" aria-busy="true">Loading story…</p>
        </Match>
        <Match when={status() === "error"}>
          <div class="reader-status reader-status-error" role="alert">
            <p>This story could not be loaded.</p>
            <p><a href={routeHref("/")}>Back home</a></p>
          </div>
        </Match>
        <Match when={status() === "ready"}>
          <div class="reader-text" lang={language()}>
            <For each={tokens()}>
              {(token) => (
                <Show
                  when={token.is_word && token.key}
                  fallback={<span class="reader-gap">{token.surface}</span>}
                >
                  <span
                    class="reader-word"
                    data-level={dataLevel(displayLevel(token))}
                    ref={(el) => (wordEls[token.position] = el)}
                    onClick={() => setReaderCursorPosition(token.position)}
                  >
                    {token.surface}
                  </span>
                </Show>
              )}
            </For>
          </div>
        </Match>
      </Switch>

      <Show when={popupVisible() ? currentToken() : undefined}>
        {(token) => (
          <Show when={token().key}>
            {(key) => (
              <aside class="reader-popup" style={popupStyle()} role="dialog" aria-label="Word definition" ref={(el) => (popupEl = el)}>
                <button class="reader-popup-close" type="button" aria-label="Close" onClick={() => setPopupVisible(false)}>
                  ×
                </button>
                <p class="reader-popup-surface" lang={language()}>{token().surface}</p>
                <p class="reader-popup-key" lang={language()}>{key()}</p>
                {definitionArea(key())}
                <div class="reader-levels" role="group" aria-label="Knowledge level">
                  <For each={LEVELS}>
                    {(level) => (
                      <button
                        type="button"
                        class="reader-level"
                        data-level={dataLevel(level.value)}
                        data-active={surfaceLevel(token()) === level.value ? "" : undefined}
                        title={level.hint}
                        onClick={(event) => {
                          rate(level.value, level.event);
                          event.currentTarget.blur();
                        }}
                      >
                        {level.label}
                      </button>
                    )}
                  </For>
                </div>
                <div class="reader-canonical-actions" role="group" aria-label="Lemma/root knowledge">
                  <button
                    type="button"
                    class="reader-deep"
                    data-active={knowledge[key()]?.level === "well_known" ? "" : undefined}
                    onClick={(event) => {
                      markCanonical("well_known");
                      event.currentTarget.blur();
                    }}
                  >
                    Mark lemma known
                  </button>
                  <button
                    type="button"
                    class="reader-deep"
                    data-active={knowledge[key()]?.level === "ignored" ? "" : undefined}
                    onClick={(event) => {
                      markCanonical("ignored");
                      event.currentTarget.blur();
                    }}
                  >
                    Ignore lemma
                  </button>
                  <Show when={knowledge[key()]?.level === "well_known" || knowledge[key()]?.level === "ignored"}>
                    <button
                      type="button"
                      class="reader-deep"
                      onClick={(event) => {
                        markCanonical("");
                        event.currentTarget.blur();
                      }}
                    >
                      Clear lemma mark
                    </button>
                  </Show>
                </div>
                <button
                  type="button"
                  class="reader-deep"
                  onClick={(event) => {
                    openWordBreakdown(key());
                    event.currentTarget.blur();
                  }}
                >
                  Deep breakdown
                </button>
                <Show when={(knowledge[key()]?.lookup_count ?? 0) > 0}>
                  <p class="reader-popup-meta">Looked up {knowledge[key()]?.lookup_count}×</p>
                </Show>
              </aside>
            )}
          </Show>
        )}
      </Show>

      <Show when={analysis()}>
        {(active) => (
          <aside
            class="reader-breakdown"
            role="dialog"
            aria-label={active().mode === "sentence" ? "Sentence breakdown" : "Word breakdown"}
          >
            <header class="reader-breakdown-head">
              <h2>{active().mode === "sentence" ? "Sentence" : "Word"} breakdown</h2>
              <button type="button" aria-label="Close" onClick={() => setAnalysis(null)}>×</button>
            </header>
            {breakdownBody(breakdownEntry(active()), active().mode)}
          </aside>
        )}
      </Show>
    </section>
  );
}

function readSavedCursor(storyId: string): number | undefined {
  try {
    const raw = localStorage.getItem(readerPositionKey(storyId));
    if (raw === null) {
      return undefined;
    }
    const value = Number(raw);
    return Number.isSafeInteger(value) ? value : undefined;
  } catch {
    return undefined;
  }
}

function writeSavedCursor(storyId: string, position: number) {
  try {
    localStorage.setItem(readerPositionKey(storyId), String(position));
  } catch {
    // Reading should continue even when localStorage is unavailable.
  }
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

function definitionBody(entry: Loadable<Definition> | undefined, lang: string): JSX.Element {
  if (!entry || entry === "loading") {
    return <p class="reader-popup-loading">Looking up…</p>;
  }
  if (entry === "error") {
    return <p class="reader-popup-error">No definition available.</p>;
  }
  return (
    <>
      <p class="reader-gloss">{entry.gloss}</p>
      <Show when={entry.grammatical_note}>
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
