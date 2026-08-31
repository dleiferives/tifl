import { createEffect, createSignal, For, onCleanup, Show } from "solid-js";
import {
  APIError,
  getConversation,
  respondToConversation,
  startConversation,
  type Conversation,
} from "../api";
import { routeHref } from "../router";
import { appStore } from "../store";

type ConversationTurn = Conversation["turns"][number];

export function ConversationView(props: { conversationId?: string }) {
  const [conversation, setConversation] = createSignal<Conversation | null>(null);
  const [input, setInput] = createSignal("");
  const [loading, setLoading] = createSignal(Boolean(props.conversationId));
  const [submitting, setSubmitting] = createSignal(false);
  const [error, setError] = createSignal("");
  const [showGreek, setShowGreek] = createSignal(true);
  const [readerFocus, setReaderFocus] = createSignal(false);
  const [speakingTurn, setSpeakingTurn] = createSignal("");

  let loadedConversationID: string | undefined | null = null;
  createEffect(() => {
    const conversationID = props.conversationId;
    if (conversationID === loadedConversationID) return;
    loadedConversationID = conversationID;
    setConversation(null);
    setInput("");
    if (conversationID) {
      void load(conversationID);
    } else {
      setLoading(false);
      setError("");
    }
  });
  onCleanup(() => window.speechSynthesis?.cancel());

  const load = async (conversationID: string) => {
    setLoading(true);
    setError("");
    const finish = appStore.beginOperation();
    try {
      setConversation(await getConversation(conversationID));
    } catch (cause) {
      setError(conversationError(cause, "load this conversation"));
    } finally {
      finish();
      setLoading(false);
    }
  };

  const start = async () => {
    setSubmitting(true);
    setError("");
    const finish = appStore.beginOperation();
    try {
      const next = await startConversation();
      setConversation(next);
      window.location.hash = routeHref(`/conversation/${encodeURIComponent(next.conversation_id)}`);
    } catch (cause) {
      setError(conversationError(cause, "start a Greek story"));
    } finally {
      finish();
      setSubmitting(false);
    }
  };

  const respond = async () => {
    const current = conversation();
    const text = input().trim();
    if (!current || !text || submitting()) {
      return;
    }
    setSubmitting(true);
    setError("");
    const finish = appStore.beginOperation();
    try {
      setConversation(await respondToConversation(current.conversation_id, { text }));
      setInput("");
    } catch (cause) {
      setError(conversationError(cause, "send your response"));
    } finally {
      finish();
      setSubmitting(false);
    }
  };

  const speak = (turn: ConversationTurn) => {
    if (!("speechSynthesis" in window) || !turn.greek_text) {
      setError("This browser does not provide a Greek device voice.");
      return;
    }
    window.speechSynthesis.cancel();
    const utterance = new SpeechSynthesisUtterance(turn.greek_text);
    utterance.lang = "el-GR";
    utterance.rate = 0.82;
    utterance.onend = () => setSpeakingTurn("");
    utterance.onerror = () => {
      setSpeakingTurn("");
      setError("The device voice could not play this passage.");
    };
    setSpeakingTurn(turn.turn_id);
    window.speechSynthesis.speak(utterance);
  };

  return (
    <section class={`conversation-view${readerFocus() ? " reader-focus" : ""}`}>
      <header class="view-heading conversation-heading">
        <div>
          <p class="conversation-eyebrow">Modern Greek · adaptive input</p>
          <h1>Story coach</h1>
          <p>A story that gets simpler when something is unclear, then returns to where you left off.</p>
        </div>
        <Show when={conversation()}>
          <a class="button-link secondary-link" href={routeHref("/conversation")}>New story</a>
        </Show>
      </header>

      <Show when={error()}>
        <p class="form-error" role="alert">{error()}</p>
      </Show>

      <Show when={!props.conversationId && !conversation()}>
        <div class="conversation-intro">
          <div>
            <h2>Listen, interpret, repair, continue</h2>
            <p>The coach gives you a short Greek passage. Tell it what you understood in English and name anything that was unclear.</p>
            <p>When you miss something, it opens a smaller story around that exact gap. Once it clicks, you climb back to the original passage.</p>
          </div>
          <button class="primary-button" type="button" disabled={submitting()} onClick={() => void start()}>
            {submitting() ? "Writing the opening…" : "Start a Greek story"}
          </button>
        </div>
      </Show>

      <Show when={loading()}>
        <div class="conversation-state" aria-busy="true">Loading the story…</div>
      </Show>

      <Show when={conversation()}>
        {(detail) => (
          <>
            <div class="conversation-toolbar" aria-label="Reading controls">
              <button class="secondary-button" type="button" aria-pressed={showGreek()} onClick={() => setShowGreek(!showGreek())}>
                {showGreek() ? "Hide Greek" : "Show Greek"}
              </button>
              <button class="secondary-button" type="button" aria-pressed={readerFocus()} onClick={() => setReaderFocus(!readerFocus())}>
                {readerFocus() ? "Exit reader focus" : "Reader focus"}
              </button>
              <span class="repair-depth" data-active={detail().repair_depth > 0}>
                {detail().repair_depth > 0 ? `Repair depth ${detail().repair_depth}` : "Main story"}
              </span>
            </div>

            <div class="conversation-turns" aria-live="polite">
              <For each={detail().turns}>
                {(turn) => (
                  <article class="conversation-turn" data-role={turn.role} data-kind={turn.kind}>
                    <Show when={turn.role === "assistant"} fallback={<p class="learner-response">{turn.input_text || turn.transcript}</p>}>
                      <div class="turn-meta">
                        <span>{turnLabel(turn)}</span>
                        <Show when={turn.focus}><span class="focus-chip">Focus: {turn.focus}</span></Show>
                      </div>
                      <Show when={showGreek()} fallback={<p class="greek-hidden">Greek text hidden — listen first, then reveal it when you want.</p>}>
                        <p class="greek-passage" lang="el">{turn.greek_text}</p>
                      </Show>
                      <div class="turn-actions">
                        <button class="secondary-button" type="button" disabled={speakingTurn() === turn.turn_id} onClick={() => speak(turn)}>
                          {speakingTurn() === turn.turn_id ? "Playing…" : "Listen"}
                        </button>
                      </div>
                      <Show when={turn.english_text}><p class="english-feedback">{turn.english_text}</p></Show>
                      <Show when={turn.prompt_text}><p class="conversation-prompt">{turn.prompt_text}</p></Show>
                    </Show>
                  </article>
                )}
              </For>
            </div>

            <form class="conversation-response" onSubmit={(event) => { event.preventDefault(); void respond(); }}>
              <label for="conversation-response">What did you understand?</label>
              <textarea
                id="conversation-response"
                rows="4"
                value={input()}
                disabled={submitting()}
                placeholder="Give your best English translation. You can also say exactly what you didn't understand."
                onInput={(event) => setInput(event.currentTarget.value)}
                onKeyDown={(event) => {
                  if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
                    event.preventDefault();
                    void respond();
                  }
                }}
              />
              <div class="conversation-submit-row">
                <p>Type for now · speech input plugs into this same turn next.</p>
                <button class="primary-button" type="submit" disabled={!input().trim() || submitting()}>
                  {submitting() ? "Thinking…" : "Send response"}
                </button>
              </div>
            </form>
          </>
        )}
      </Show>
    </section>
  );
}

function turnLabel(turn: ConversationTurn): string {
  switch (turn.kind) {
    case "repair_story": return "Focused sub-story";
    case "retry": return "Back to the parent story";
    default: return "Story";
  }
}

function conversationError(cause: unknown, action: string): string {
  if (cause instanceof APIError) {
    if (cause.status === 503) return "The Greek story coach needs an LLM connection before it can run.";
    if (cause.status === 404) return "That conversation could not be found.";
    return cause.message;
  }
  return cause instanceof Error ? cause.message : `Could not ${action}.`;
}
