import { createEffect, createSignal, For, Match, onCleanup, onMount, Show, Switch } from "solid-js";
import type { JSX } from "solid-js";
import { createStore } from "solid-js/store";
import {
  getDefinition,
  getStory,
  postReaderEvents,
  sentenceBreakdown,
  setWordKnowledge,
  wordBreakdown,
} from "../api";
import type { APISchema } from "../api";
import { routeHref } from "../router";
import { appStore } from "../store";

type StoryToken = APISchema<"StoryToken">;
type ReaderKnowledge = APISchema<"ReaderKnowledge">;
type KnowledgeLevel = ReaderKnowledge["level"];
type Definition = APISchema<"Definition">;
type ReaderEvent = APISchema<"ReaderEvent">;

type Loadable<T> = "loading" | "error" | T;
type Analysis = { mode: "sentence"; position: number } | { mode: "word"; key: string } | null;

const FLUSH_DELAY_MS = 4000;

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

export function ReaderView(props: { storyId: string }) {
  const [status, setStatus] = createSignal<"loading" | "ready" | "error">("loading");
  const [tokens, setTokens] = createSignal<StoryToken[]>([]);
  const [language, setLanguage] = createSignal("");
  const [knowledge, setKnowledge] = createStore<Record<string, ReaderKnowledge>>({});
  const [cursor, setCursor] = createSignal(0);
  const [popupVisible, setPopupVisible] = createSignal(false);
  const [popupPos, setPopupPos] = createSignal<{ top: number; left: number } | null>(null);
  const [definitions, setDefinitions] = createStore<Record<string, Loadable<Definition>>>({});
  const [analysis, setAnalysis] = createSignal<Analysis>(null);
  const [sentences, setSentences] = createStore<Record<number, Loadable<unknown>>>({});
  const [words, setWords] = createStore<Record<string, Loadable<unknown>>>({});

  // One element ref per word token, indexed by token position. Used to move the
  // cursor highlight surgically (two DOM writes) and to anchor the popup.
  const wordEls: (HTMLElement | undefined)[] = [];
  let pending: ReaderEvent[] = [];
  let flushTimer: number | undefined;

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
    void flush(true);
  });

  async function load() {
    try {
      const story = await getStory(props.storyId);
      // Seed every word key so the per-span knowledge subscription is fine-grained
      // from the first render; words the user never touched stay unseen ("").
      const seeded: Record<string, ReaderKnowledge> = {};
      for (const token of story.tokens) {
        if (token.is_word && token.key) {
          seeded[token.key] = story.knowledge[token.key] ?? { level: "", lookup_count: 0 };
        }
      }
      setKnowledge(seeded);
      setTokens(story.tokens);
      setLanguage(story.language);
      const first = story.tokens.findIndex((token) => token.is_word && token.key);
      setCursor(first < 0 ? 0 : first);
      setStatus("ready");
    } catch {
      setStatus("error");
    }
  }

  // Cursor highlight: clear the previous word, mark the new one. Two writes.
  let prevCursor: number | undefined;
  createEffect(() => {
    const c = cursor();
    if (status() !== "ready") {
      return;
    }
    if (prevCursor !== undefined && prevCursor !== c) {
      wordEls[prevCursor]?.removeAttribute("data-cursor");
    }
    const el = wordEls[c];
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
    const list = tokens();
    for (let i = cursor() + direction; i >= 0 && i < list.length; i += direction) {
      if (list[i].is_word && list[i].key) {
        setCursor(i);
        return;
      }
    }
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
    const el = wordEls[cursor()];
    if (!el) {
      setPopupPos(null);
      return;
    }
    const rect = el.getBoundingClientRect();
    const left = Math.max(8, Math.min(rect.left, window.innerWidth - 328));
    setPopupPos({ top: rect.bottom + 8, left });
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

  function rate(level: KnowledgeLevel, eventValue: string) {
    const token = currentToken();
    if (!token?.key) {
      return;
    }
    const key = token.key;
    const previous = knowledge[key]?.level ?? "";
    setKnowledge(key, "level", level); // optimistic; cursor stays put
    void setWordKnowledge(key, { language: language(), level }).catch(() => {
      setKnowledge(key, "level", previous);
      appStore.showToast("That rating could not be saved.", "error");
    });
    enqueue({ event_type: "rate", position: token.position, value: eventValue });
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
    if (status() !== "ready") {
      return;
    }
    const target = event.target as HTMLElement | null;
    if (target?.closest("input, textarea, select")) {
      return;
    }
    switch (event.key) {
      case "ArrowRight":
        event.preventDefault();
        moveCursor(1);
        break;
      case "ArrowLeft":
        event.preventDefault();
        moveCursor(-1);
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
      case "S":
        event.preventDefault();
        openSentenceBreakdown();
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
          <span><kbd>←</kbd> <kbd>→</kbd> move</span>
          <span><kbd>Space</kbd> define</span>
          <span><kbd>1</kbd>–<kbd>5</kbd> rate</span>
          <span><kbd>w</kbd> known</span>
          <span><kbd>i</kbd> ignore</span>
          <span><kbd>s</kbd> sentence</span>
        </p>
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
                    data-level={dataLevel(knowledge[token.key as string]?.level)}
                    ref={(el) => (wordEls[token.position] = el)}
                    onClick={() => setCursor(token.position)}
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
              <aside class="reader-popup" style={popupStyle()} role="dialog" aria-label="Word definition">
                <button class="reader-popup-close" type="button" aria-label="Close" onClick={() => setPopupVisible(false)}>
                  ×
                </button>
                <p class="reader-popup-surface" lang={language()}>{token().surface}</p>
                <p class="reader-popup-key" lang={language()}>{key()}</p>
                {definitionBody(definitions[key()], language())}
                <div class="reader-levels" role="group" aria-label="Knowledge level">
                  <For each={LEVELS}>
                    {(level) => (
                      <button
                        type="button"
                        class="reader-level"
                        data-level={dataLevel(level.value)}
                        data-active={knowledge[key()]?.level === level.value ? "" : undefined}
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
            {breakdownBody(breakdownEntry(active()))}
          </aside>
        )}
      </Show>
    </section>
  );
}

function definitionBody(entry: Loadable<Definition> | undefined, lang: string): JSX.Element {
  if (!entry || entry === "loading") {
    return <p class="reader-popup-loading">Looking up…</p>;
  }
  if (entry === "error") {
    return <p class="reader-popup-error">No definition available.</p>;
  }
  return (
    <div class="reader-popup-def">
      <p class="reader-gloss">{entry.gloss}</p>
      <Show when={entry.grammatical_note}>
        <p class="reader-grammar">{entry.grammatical_note}</p>
      </Show>
      <Show when={entry.example}>
        <p class="reader-example" lang={lang}>{entry.example}</p>
      </Show>
      <span class="reader-source" data-source={entry.source}>{entry.source}</span>
    </div>
  );
}

function breakdownBody(entry: Loadable<unknown> | undefined): JSX.Element {
  if (!entry || entry === "loading") {
    return <p class="reader-breakdown-loading" aria-busy="true">Analyzing…</p>;
  }
  if (entry === "error") {
    return <p class="reader-breakdown-error">This breakdown is unavailable right now.</p>;
  }
  return <div class="reader-json">{renderJSON(entry)}</div>;
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
