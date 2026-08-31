import { createEffect, createSignal, For, onCleanup, Show } from "solid-js";
import {
  APIError,
  getConversation,
  getConversationTurnAudio,
  respondToConversation,
  respondToConversationAudio,
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
  const [audioFirst, setAudioFirst] = createSignal(false);
  const [speakingTurn, setSpeakingTurn] = createSignal("");
  const [recording, setRecording] = createSignal(false);
  const [requestingMicrophone, setRequestingMicrophone] = createSignal(false);
  let mediaRecorder: MediaRecorder | null = null;
  let microphoneStream: MediaStream | null = null;
  let audioElement: HTMLAudioElement | null = null;
  let playbackToken = 0;
  let microphoneRequestToken = 0;
  let disposed = false;
  const audioObjectURLs = new Map<string, string>();

  const resetTransientMedia = () => {
    playbackToken++;
    microphoneRequestToken++;
    audioElement?.pause();
    audioElement = null;
    setSpeakingTurn("");
    if (mediaRecorder && mediaRecorder.state !== "inactive") {
      mediaRecorder.onstop = null;
      mediaRecorder.stop();
    }
    microphoneStream?.getTracks().forEach((track) => track.stop());
    mediaRecorder = null;
    microphoneStream = null;
    setRecording(false);
    setRequestingMicrophone(false);
    for (const objectURL of audioObjectURLs.values()) URL.revokeObjectURL(objectURL);
    audioObjectURLs.clear();
  };

  let loadedConversationID: string | undefined | null = null;
  createEffect(() => {
    const conversationID = props.conversationId;
    if (conversationID === loadedConversationID) return;
    loadedConversationID = conversationID;
    resetTransientMedia();
    setConversation(null);
    setInput("");
    if (conversationID) {
      void load(conversationID);
    } else {
      setLoading(false);
      setError("");
    }
  });
  onCleanup(() => {
    disposed = true;
    resetTransientMedia();
  });

  const load = async (conversationID: string) => {
    setLoading(true);
    setError("");
    const finish = appStore.beginOperation();
    try {
      const next = await getConversation(conversationID);
      setConversation(next);
      playLatestWhenEnabled(next);
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
      const next = await respondToConversation(current.conversation_id, { text });
      setConversation(next);
      setInput("");
      playLatestWhenEnabled(next);
    } catch (cause) {
      setError(conversationError(cause, "send your response"));
    } finally {
      finish();
      setSubmitting(false);
    }
  };

  const speak = async (turn: ConversationTurn) => {
    if (!turn.audio_url) {
      setError("Server speech is not configured for this conversation.");
      return;
    }
    const token = ++playbackToken;
    audioElement?.pause();
    setSpeakingTurn(turn.turn_id);
    setError("");
    try {
      let objectURL = audioObjectURLs.get(turn.turn_id);
      if (!objectURL) {
        const audio = await getConversationTurnAudio(turn.audio_url);
        if (disposed || token !== playbackToken) return;
        objectURL = URL.createObjectURL(audio);
        audioObjectURLs.set(turn.turn_id, objectURL);
      }
      if (disposed || token !== playbackToken) return;
      const player = new Audio(objectURL);
      audioElement = player;
      player.onended = () => {
        if (token === playbackToken) setSpeakingTurn("");
      };
      player.onerror = () => {
        if (token === playbackToken) {
          setSpeakingTurn("");
          setError("The generated Greek audio could not be played.");
        }
      };
      await player.play();
    } catch (cause) {
      if (token === playbackToken) {
        setSpeakingTurn("");
        setError(conversationError(cause, "play this passage"));
      }
    }
  };

  const playLatestWhenEnabled = (detail: Conversation) => {
    if (!audioFirst()) return;
    const latest = [...detail.turns].reverse().find((turn) => turn.role === "assistant");
    if (latest) void speak(latest);
  };

  const toggleAudioFirst = () => {
    const enabled = !audioFirst();
    setAudioFirst(enabled);
    if (!enabled) return;
    setShowGreek(false);
    const detail = conversation();
    if (detail) playLatestWhenEnabled(detail);
  };

  const startRecording = async () => {
    if (recording() || requestingMicrophone()) return;
    if (!navigator.mediaDevices?.getUserMedia || !("MediaRecorder" in window)) {
      setError("This browser does not support microphone recording.");
      return;
    }
    const token = ++microphoneRequestToken;
    setRequestingMicrophone(true);
    setError("");
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      if (disposed || token !== microphoneRequestToken) {
        stream.getTracks().forEach((track) => track.stop());
        return;
      }
      microphoneStream = stream;
      const candidates = ["audio/webm;codecs=opus", "audio/mp4", "audio/ogg;codecs=opus"];
      const mimeType = candidates.find((candidate) => MediaRecorder.isTypeSupported(candidate));
      mediaRecorder = mimeType
        ? new MediaRecorder(microphoneStream, { mimeType })
        : new MediaRecorder(microphoneStream);
      const chunks: BlobPart[] = [];
      mediaRecorder.ondataavailable = (event) => {
        if (event.data.size > 0) chunks.push(event.data);
      };
      mediaRecorder.onstop = () => {
        const type = mediaRecorder?.mimeType || mimeType || "audio/webm";
        const audio = new Blob(chunks, { type });
        microphoneStream?.getTracks().forEach((track) => track.stop());
        microphoneStream = null;
        mediaRecorder = null;
        setRecording(false);
        if (!disposed) void submitRecordedAudio(audio);
      };
      mediaRecorder.start();
      setRecording(true);
    } catch (cause) {
      microphoneStream?.getTracks().forEach((track) => track.stop());
      microphoneStream = null;
      mediaRecorder = null;
      setRecording(false);
      if (!disposed && token === microphoneRequestToken) {
        setError(cause instanceof Error ? cause.message : "Microphone access failed.");
      }
    } finally {
      if (!disposed && token === microphoneRequestToken) setRequestingMicrophone(false);
    }
  };

  const stopRecording = () => {
    if (mediaRecorder?.state === "recording") mediaRecorder.stop();
  };

  const submitRecordedAudio = async (audio: Blob) => {
    const current = conversation();
    if (!current || audio.size === 0) {
      setError("No microphone audio was recorded.");
      return;
    }
    setSubmitting(true);
    setError("");
    const finish = appStore.beginOperation();
    try {
      const next = await respondToConversationAudio(current.conversation_id, audio);
      if (disposed) return;
      setConversation(next);
      playLatestWhenEnabled(next);
    } catch (cause) {
      if (!disposed) setError(conversationError(cause, "transcribe your response"));
    } finally {
      finish();
      if (!disposed) setSubmitting(false);
    }
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
              <button class="secondary-button" type="button" aria-pressed={audioFirst()} onClick={toggleAudioFirst}>
                {audioFirst() ? "Audio-first on" : "Audio-first"}
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
                        <button class="secondary-button" type="button" disabled={!turn.audio_url || speakingTurn() === turn.turn_id} onClick={() => void speak(turn)}>
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
                disabled={submitting() || recording() || requestingMicrophone()}
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
                <p aria-live="polite">
                  {recording()
                    ? "Recording your interpretation…"
                    : requestingMicrophone()
                      ? "Opening your microphone…"
                      : "Speak or type your best interpretation."}
                </p>
                <div class="conversation-response-actions">
                  <button
                    class={recording() ? "danger-button" : "secondary-button"}
                    type="button"
                    disabled={submitting() || requestingMicrophone()}
                    onClick={() => recording() ? stopRecording() : void startRecording()}
                  >
                    {recording() ? "Stop & send" : requestingMicrophone() ? "Opening…" : "Use microphone"}
                  </button>
                  <button class="primary-button" type="submit" disabled={!input().trim() || submitting() || recording() || requestingMicrophone()}>
                    {submitting() ? "Transcribing / thinking…" : "Send response"}
                  </button>
                </div>
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
    if (cause.status === 503 && cause.message.includes("generation")) {
      return "The Greek story coach needs an LLM connection before it can run.";
    }
    if (cause.status === 404) return "That conversation could not be found.";
    return cause.message;
  }
  return cause instanceof Error ? cause.message : `Could not ${action}.`;
}
