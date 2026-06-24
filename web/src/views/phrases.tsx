import { createMemo, createSignal, For, Match, onMount, Show, Switch } from "solid-js";
import { getSessionContent, type APIResponse, type APISchema } from "../api";
import { routeHref } from "../router";
import { appStore } from "../store";

type SessionContent = APIResponse<"getSessionContent", 200>;
type PhraseSet = APISchema<"PhraseSet">;
type PhraseItem = APISchema<"PhraseItem">;

// PhrasesView renders an expression-guided phrase set: the curated target-language
// phrases plus their glosses and annotations. It is the phrase-session counterpart
// to the reader — phrase sets are not tokenized, so they load through
// GET /sessions/{id}/content rather than the story endpoints. See #74 and
// context/session-types.md ("Phrase set").
export function PhrasesView(props: { sessionId: string }) {
  const [status, setStatus] = createSignal<"loading" | "ready" | "error">("loading");
  const [content, setContent] = createSignal<SessionContent | null>(null);

  onMount(() => void load());

  async function load() {
    setStatus("loading");
    const finish = appStore.beginOperation();
    try {
      const data = await getSessionContent(props.sessionId);
      setContent(data);
      setStatus("ready");
    } catch {
      setStatus("error");
    } finally {
      finish();
    }
  }

  const phraseSet = createMemo<PhraseSet | null>(() => content()?.phrase_set ?? null);
  const items = createMemo<PhraseItem[]>(() => phraseSet()?.items ?? []);
  // A story session reached here by mistake (e.g. a stale link): offer the reader.
  const storyId = createMemo(() => content()?.story?.story_id ?? "");

  return (
    <section class="phrases-view">
      <header class="view-heading">
        <div>
          <h1>Phrases</h1>
          <p>{summary(items().length, status())}</p>
        </div>
        <div class="view-heading-actions">
          <a class="button-link secondary-link" href={routeHref(`/tasks/${encodeURIComponent(props.sessionId)}`)}>Practice</a>
          <a class="button-link secondary-link" href={routeHref("/")}>Back home</a>
        </div>
      </header>

      <Switch>
        <Match when={status() === "loading"}>
          <div class="tasks-state" aria-busy="true">Loading phrases…</div>
        </Match>
        <Match when={status() === "error"}>
          <div class="tasks-state" role="alert">
            <p>This phrase set could not be loaded.</p>
            <button class="secondary-button" type="button" onClick={() => void load()}>Retry</button>
          </div>
        </Match>
        <Match when={storyId() !== ""}>
          <div class="tasks-state empty-state">
            <h2>This session is a story</h2>
            <p>Open it in the reader instead.</p>
            <a class="button-link" href={routeHref(`/reader/${encodeURIComponent(storyId())}`)}>Start reading</a>
          </div>
        </Match>
        <Match when={items().length === 0}>
          <div class="tasks-state empty-state">
            <h2>No phrases yet</h2>
            <p>This session has no phrase content attached.</p>
          </div>
        </Match>
        <Match when={items().length > 0}>
          <ol class="phrase-list">
            <For each={items()}>
              {(item, index) => (
                <li>
                  <PhraseCard item={item} position={index() + 1} lang={phraseSet()?.language ?? ""} />
                </li>
              )}
            </For>
          </ol>
        </Match>
      </Switch>
    </section>
  );
}

function PhraseCard(props: { item: PhraseItem; position: number; lang: string }) {
  const annotations = () => props.item.annotations ?? [];
  return (
    <article class="phrase-card">
      <p class="phrase-target" lang={props.lang || undefined}>{props.item.target_text}</p>
      <Show when={props.item.gloss}>
        <p class="phrase-gloss">{props.item.gloss}</p>
      </Show>
      <Show when={props.item.notes}>
        <p class="phrase-notes">{props.item.notes}</p>
      </Show>
      <Show when={annotations().length > 0}>
        <ul class="phrase-annotations">
          <For each={annotations()}>
            {(annotation) => (
              <li class="phrase-annotation">
                <Show when={annotation.kind}>
                  <span class="phrase-annotation-kind">{annotation.kind}</span>
                </Show>
                <Show when={annotation.label}>
                  <span class="phrase-annotation-label">{annotation.label}</span>
                </Show>
                <Show when={annotation.note}>
                  <span class="phrase-annotation-note">{annotation.note}</span>
                </Show>
              </li>
            )}
          </For>
        </ul>
      </Show>
    </article>
  );
}

function summary(count: number, status: "loading" | "ready" | "error"): string {
  if (status === "loading") {
    return "Loading your phrase set…";
  }
  if (status === "error") {
    return "We couldn't load this phrase set.";
  }
  if (count === 0) {
    return "No phrases attached to this session.";
  }
  return `${count} ${count === 1 ? "phrase" : "phrases"} to practise saying.`;
}
