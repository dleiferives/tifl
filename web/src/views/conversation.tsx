import { createEffect, createSignal, For, onCleanup, Show } from "solid-js";
import {
  APIError,
  getConversation,
  getConversationTurnAudio,
  listConversations,
  respondToConversation,
  respondToConversationAudio,
  startConversation,
  transcribeConversationAudio,
  type Conversation,
  type ConversationSummary,
} from "../api";
import { routeHref } from "../router";
import { appStore } from "../store";

type ConversationTurn = Conversation["turns"][number];

export function ConversationView(props: { conversationId?: string }) {
  const [conversation, setConversation] = createSignal<Conversation | null>(null);
  const [conversationHistory, setConversationHistory] = createSignal<ConversationSummary[]>([]);
  const [historyLoading, setHistoryLoading] = createSignal(false);
  const [topic, setTopic] = createSignal("");
  const [sourceText, setSourceText] = createSignal("");
  const [input, setInput] = createSignal("");
  const [loading, setLoading] = createSignal(Boolean(props.conversationId));
  const [submitting, setSubmitting] = createSignal(false);
  const [error, setError] = createSignal("");
  const [showGreek, setShowGreek] = createSignal(true);
  const [readerFocus, setReaderFocus] = createSignal(false);
  const [audioFirst, setAudioFirst] = createSignal(false);
  const [fullAuto, setFullAuto] = createSignal(false);
  const [autoStatus, setAutoStatus] = createSignal("");
  const [speakingTurn, setSpeakingTurn] = createSignal("");
  const [recording, setRecording] = createSignal(false);
  const [requestingMicrophone, setRequestingMicrophone] = createSignal(false);
  let mediaRecorder: MediaRecorder | null = null;
  let microphoneStream: MediaStream | null = null;
  let audioElement: HTMLAudioElement | null = null;
  let playbackToken = 0;
  let microphoneRequestToken = 0;
  let autoCycleToken = 0;
  let autoAnimationFrame = 0;
  let autoAudioContext: AudioContext | null = null;
  let disposed = false;
  const audioObjectURLs = new Map<string, string>();

  const resetTransientMedia = () => {
    playbackToken++;
    microphoneRequestToken++;
    autoCycleToken++;
    cancelAnimationFrame(autoAnimationFrame);
    autoAnimationFrame = 0;
    void autoAudioContext?.close();
    autoAudioContext = null;
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
    setAutoStatus("");
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
      void loadHistory();
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

  const loadHistory = async () => {
    setHistoryLoading(true);
    setError("");
    const finish = appStore.beginOperation();
    try {
      setConversationHistory((await listConversations()).conversations);
    } catch (cause) {
      setError(conversationError(cause, "load earlier conversations"));
    } finally {
      finish();
      setHistoryLoading(false);
    }
  };

  const start = async () => {
    setSubmitting(true);
    setError("");
    const finish = appStore.beginOperation();
    try {
      const next = await startConversation({
        ...(topic().trim() ? { topic: topic().trim() } : {}),
        ...(sourceText().trim() ? { source_text: sourceText().trim() } : {}),
      });
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
      if (fullAuto()) {
        void beginAutoCycle(next);
      } else {
        playLatestWhenEnabled(next);
      }
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
      await playTurnPart(turn, "passage", token);
    } catch (cause) {
      if (token === playbackToken) {
        setSpeakingTurn("");
        setError(conversationError(cause, "play this passage"));
      }
    } finally {
      if (token === playbackToken) setSpeakingTurn("");
    }
  };

  const playTurnPart = async (
    turn: ConversationTurn,
    part: "passage" | "feedback" | "prompt",
    token: number,
  ) => {
    if (!turn.audio_url) throw new Error("Server speech is not configured for this conversation.");
    const key = `${turn.turn_id}:${part}`;
    let objectURL = audioObjectURLs.get(key);
    if (!objectURL) {
      const separator = turn.audio_url.includes("?") ? "&" : "?";
      const audio = await getConversationTurnAudio(`${turn.audio_url}${separator}part=${part}`);
      if (disposed || token !== playbackToken) return;
      objectURL = URL.createObjectURL(audio);
      audioObjectURLs.set(key, objectURL);
    }
    if (disposed || token !== playbackToken) return;
    await new Promise<void>((resolve, reject) => {
      const player = new Audio(objectURL);
      audioElement = player;
      player.onended = () => resolve();
      player.onerror = () => reject(new Error("The generated coaching audio could not be played."));
      player.play().catch(reject);
    });
  };

  const narrateTurn = async (turn: ConversationTurn, token: number) => {
    const parts: Array<"passage" | "feedback" | "prompt"> = [];
    if (turn.english_text) parts.push("feedback");
    if (turn.greek_text) parts.push("passage");
    if (turn.prompt_text) parts.push("prompt");
    setSpeakingTurn(turn.turn_id);
    setAutoStatus("Playing the coach’s turn…");
    for (const part of parts) {
      if (!fullAuto() || token !== autoCycleToken) return;
      await playTurnPart(turn, part, playbackToken);
    }
    if (token === autoCycleToken) setSpeakingTurn("");
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

  const toggleFullAuto = () => {
    const enabled = !fullAuto();
    setFullAuto(enabled);
    if (!enabled) {
      resetTransientMedia();
      return;
    }
    setAudioFirst(false);
    setShowGreek(false);
    const detail = conversation();
    if (detail) void beginAutoCycle(detail);
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

  const beginAutoCycle = async (detail: Conversation) => {
    const latest = [...detail.turns].reverse().find((turn) => turn.role === "assistant");
    if (!latest || !fullAuto()) return;
    const cycle = ++autoCycleToken;
    const playToken = ++playbackToken;
    audioElement?.pause();
    setError("");
    try {
      await narrateTurn(latest, cycle);
      if (!fullAuto() || cycle !== autoCycleToken || playToken !== playbackToken) return;
      await collectAutoResponse(detail, latest, cycle);
    } catch (cause) {
      if (fullAuto() && cycle === autoCycleToken) {
        setAutoStatus("");
        setSpeakingTurn("");
        setError(conversationError(cause, "run Full auto mode"));
      }
    }
  };

  const collectAutoResponse = async (
    detail: Conversation,
    turn: ConversationTurn,
    cycle: number,
  ) => {
    let accumulated = input().trim();
    while (fullAuto() && cycle === autoCycleToken) {
      setAutoStatus(accumulated
        ? "Keep speaking, then say “pass” when your answer is ready."
        : "Listening — answer in English, say “repeat” to replay, or “pass” when done.");
      const audio = await recordAutoChunk(cycle);
      if (!fullAuto() || cycle !== autoCycleToken) return;
      setAutoStatus("Transcribing…");
      const transcript = (await transcribeConversationAudio(detail.conversation_id, audio)).text.trim();
      if (!fullAuto() || cycle !== autoCycleToken) return;
      const command = normalizeVoiceCommand(transcript);
      if (command === "repeat") {
        const playToken = ++playbackToken;
        audioElement?.pause();
        await narrateTurn(turn, cycle);
        if (playToken !== playbackToken) return;
        continue;
      }

      const passed = /(?:^|\s)pass[.!?\s]*$/i.test(transcript);
      const spoken = passed ? transcript.replace(/(?:^|\s)pass[.!?\s]*$/i, "").trim() : transcript;
      if (spoken) {
        accumulated = [accumulated, spoken].filter(Boolean).join(" ");
        setInput(accumulated);
      }
      if (!passed) continue;

      setAutoStatus("Coach is thinking…");
      setSubmitting(true);
      const finish = appStore.beginOperation();
      let next: Conversation;
      try {
        next = await respondToConversation(detail.conversation_id, {
          text: accumulated || "I don't know what that meant.",
        });
      } finally {
        finish();
        setSubmitting(false);
      }
      if (!fullAuto() || cycle !== autoCycleToken) return;
      setConversation(next);
      setInput("");
      void beginAutoCycle(next);
      return;
    }
  };

  const recordAutoChunk = async (cycle: number): Promise<Blob> => {
    if (!navigator.mediaDevices?.getUserMedia || !("MediaRecorder" in window)) {
      throw new Error("This browser does not support microphone recording.");
    }
    const requestToken = ++microphoneRequestToken;
    setRequestingMicrophone(true);
    let stream: MediaStream;
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    } finally {
      setRequestingMicrophone(false);
    }
    if (disposed || !fullAuto() || cycle !== autoCycleToken || requestToken !== microphoneRequestToken) {
      stream.getTracks().forEach((track) => track.stop());
      throw new Error("Full auto listening was cancelled.");
    }
    microphoneStream = stream;
    const candidates = ["audio/webm;codecs=opus", "audio/mp4", "audio/ogg;codecs=opus"];
    const mimeType = candidates.find((candidate) => MediaRecorder.isTypeSupported(candidate));
    const recorder = mimeType ? new MediaRecorder(stream, { mimeType }) : new MediaRecorder(stream);
    mediaRecorder = recorder;
    const chunks: BlobPart[] = [];
    recorder.addEventListener("dataavailable", (event) => {
      if (event.data.size > 0) chunks.push(event.data);
    });
    const stopped = new Promise<Blob>((resolve) => {
      recorder.addEventListener("stop", () => {
        cancelAnimationFrame(autoAnimationFrame);
        autoAnimationFrame = 0;
        stream.getTracks().forEach((track) => track.stop());
        if (microphoneStream === stream) microphoneStream = null;
        if (mediaRecorder === recorder) mediaRecorder = null;
        setRecording(false);
        void autoAudioContext?.close();
        autoAudioContext = null;
        resolve(new Blob(chunks, { type: recorder.mimeType || mimeType || "audio/webm" }));
      }, { once: true });
    });

    const AudioContextClass = window.AudioContext;
    autoAudioContext = new AudioContextClass();
    const analyser = autoAudioContext.createAnalyser();
    analyser.fftSize = 1024;
    autoAudioContext.createMediaStreamSource(stream).connect(analyser);
    const samples = new Uint8Array(analyser.fftSize);
    const startedAt = performance.now();
    let heardSpeech = false;
    let lastSpeechAt = startedAt;
    const monitor = () => {
      if (recorder.state !== "recording") return;
      analyser.getByteTimeDomainData(samples);
      let sum = 0;
      for (const sample of samples) {
        const normalized = (sample - 128) / 128;
        sum += normalized * normalized;
      }
      const now = performance.now();
      if (Math.sqrt(sum / samples.length) > 0.025) {
        heardSpeech = true;
        lastSpeechAt = now;
      }
      if ((heardSpeech && now - lastSpeechAt > 1100) || now - startedAt > 20_000) {
        recorder.stop();
        return;
      }
      autoAnimationFrame = requestAnimationFrame(monitor);
    };
    recorder.start(250);
    setRecording(true);
    autoAnimationFrame = requestAnimationFrame(monitor);
    return stopped;
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
        <div class="conversation-home">
          <form class="conversation-starter" onSubmit={(event) => { event.preventDefault(); void start(); }}>
            <div>
              <h2>Choose what the story teaches</h2>
              <p>Give the coach a subject, paste a story or passage you want to learn, or use both. Leave both blank when you want a surprise.</p>
            </div>
            <label class="field">
              <span>Topic</span>
              <input
                type="text"
                maxlength="300"
                value={topic()}
                placeholder="e.g. ordering breakfast in Thessaloniki"
                onInput={(event) => setTopic(event.currentTarget.value)}
              />
            </label>
            <label class="field">
              <span>Existing story or text</span>
              <textarea
                rows="7"
                maxlength="30000"
                value={sourceText()}
                placeholder="Paste Greek text here. The coach will adapt it into short comprehension turns."
                onInput={(event) => setSourceText(event.currentTarget.value)}
              />
            </label>
            <div class="conversation-start-actions">
              <span>{sourceText().length.toLocaleString()} / 30,000 characters</span>
              <button class="primary-button" type="submit" disabled={submitting()}>
                {submitting() ? "Writing the opening…" : "Start story coach"}
              </button>
            </div>
          </form>

          <section class="conversation-library" aria-labelledby="conversation-library-title">
            <div class="conversation-library-heading">
              <div>
                <h2 id="conversation-library-title">Continue a story</h2>
                <p>Every thread keeps its transcript and repair depth.</p>
              </div>
              <button class="secondary-button" type="button" disabled={historyLoading()} onClick={() => void loadHistory()}>
                {historyLoading() ? "Loading…" : "Refresh"}
              </button>
            </div>
            <Show when={!historyLoading() && conversationHistory().length === 0}>
              <p class="conversation-empty">Your earlier Story Coach conversations will appear here.</p>
            </Show>
            <div class="conversation-history">
              <For each={conversationHistory()}>
                {(item) => (
                  <a class="conversation-history-card" href={routeHref(`/conversation/${encodeURIComponent(item.conversation_id)}`)}>
                    <div>
                      <strong>{item.topic || item.story_summary || "Greek story"}</strong>
                      <p>{item.story_summary || "Opening passage ready"}</p>
                    </div>
                    <div class="conversation-history-meta">
                      <span>{item.turn_count} {item.turn_count === 1 ? "turn" : "turns"}</span>
                      <span>{item.repair_depth > 0 ? `Repair depth ${item.repair_depth}` : "Main story"}</span>
                      <time datetime={new Date(item.updated_at * 1000).toISOString()}>{formatConversationDate(item.updated_at)}</time>
                    </div>
                  </a>
                )}
              </For>
            </div>
          </section>
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
              <button class="secondary-button" type="button" aria-pressed={fullAuto()} onClick={toggleFullAuto}>
                {fullAuto() ? "Stop Full auto" : "Full auto"}
              </button>
              <span class="repair-depth" data-active={detail().repair_depth > 0}>
                {detail().repair_depth > 0 ? `Repair depth ${detail().repair_depth}` : "Main story"}
              </span>
            </div>

            <Show when={fullAuto()}>
              <div class="full-auto-status" role="status" aria-live="polite">
                <span class="full-auto-indicator" data-listening={recording()} />
                <div>
                  <strong>Full auto</strong>
                  <p>{autoStatus() || "Starting the hands-free lesson…"}</p>
                </div>
              </div>
            </Show>

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
                disabled={submitting() || recording() || requestingMicrophone() || fullAuto()}
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
                    disabled={submitting() || requestingMicrophone() || fullAuto()}
                    onClick={() => recording() ? stopRecording() : void startRecording()}
                  >
                    {recording() ? "Stop & send" : requestingMicrophone() ? "Opening…" : "Use microphone"}
                  </button>
                  <button class="primary-button" type="submit" disabled={!input().trim() || submitting() || recording() || requestingMicrophone() || fullAuto()}>
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

function normalizeVoiceCommand(transcript: string): string {
  return transcript.toLocaleLowerCase().replace(/[^\p{L}\p{N}]+/gu, " ").trim();
}

function formatConversationDate(timestamp: number): string {
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  }).format(new Date(timestamp * 1000));
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
