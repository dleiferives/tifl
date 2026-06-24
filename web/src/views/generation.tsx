import { createMemo, createSignal, For, Match, onCleanup, onMount, Show, Switch } from "solid-js";
import { createStore, reconcile } from "solid-js/store";
import {
  APIError,
  getSessionDetail,
  retrySession,
  streamGenerationEvents,
  type GenerationEvent,
} from "../api";
import { routeHref } from "../router";
import { appStore } from "../store";
import { writeStartDraft, type SessionMode } from "./start";

type StageStatus = "pending" | "in_progress" | "complete" | "failed";

interface StageState {
  status: StageStatus;
}

// Stage rendering order mirrors the backend pipeline (internal/story/pipeline.go
// and the stageOrder() ordering in internal/handler/sessions.go). Task stages
// (task_<type>) share a rank and fall back to first-seen order among themselves.
const STAGE_RANK: Record<string, number> = {
  scope_check: 0,
  selection: 1,
  story_generation: 2,
  phrase_generation: 2,
  tokenization: 3,
};

const STAGE_LABELS: Record<string, string> = {
  scope_check: "Checking the topic",
  selection: "Choosing target words",
  story_generation: "Writing the story",
  phrase_generation: "Writing the phrases",
  tokenization: "Preparing the text",
};

// Friendly copy per stable error code (internal/story/pipeline.go). The raw code
// is always shown alongside for support and admin lookup.
const ERROR_MESSAGES: Record<string, string> = {
  GEN_SELECT_001: "We couldn't choose words for this session.",
  GEN_STORY_001: "The story couldn't be generated.",
  GEN_PHRASE_001: "The phrase set couldn't be generated.",
  GEN_STORY_COVERAGE: "The story didn't cover enough of your target words.",
  GEN_TOKENIZE_001: "The story couldn't be processed for reading.",
  GEN_TASK_001: "A practice task couldn't be generated.",
  GEN_PERSIST_001: "The session couldn't be saved.",
};

const TOKEN_RATE_FULL_SCALE = 80; // tok/s mapped to a full progress bar

export function GenerationView(props: { sessionId: string }) {
  const [stages, setStages] = createStore<Record<string, StageState>>({});
  const [order, setOrder] = createSignal<string[]>([]);
  const [tokenRate, setTokenRate] = createSignal(0);
  const [result, setResult] = createSignal<GenerationEvent | null>(null);
  const [connectionError, setConnectionError] = createSignal(false);
  const [retrying, setRetrying] = createSignal(false);
  const [actionError, setActionError] = createSignal("");

  let stop: (() => void) | undefined;

  const subscribe = () => {
    stop?.();
    setStages(reconcile({}));
    setOrder([]);
    setTokenRate(0);
    setResult(null);
    setConnectionError(false);
    stop = streamGenerationEvents(props.sessionId, {
      onEvent: applyEvent,
      onError: () => setConnectionError(true),
    });
  };

  const applyEvent = (event: GenerationEvent) => {
    if (event.stage === "done") {
      setResult(event);
      return;
    }
    if (!(event.stage in stages)) {
      setOrder((prev) => [...prev, event.stage]);
    }
    if (event.status) {
      setStages(event.stage, { status: event.status as StageStatus });
    } else if (!(event.stage in stages)) {
      setStages(event.stage, { status: "in_progress" });
    }
    if (typeof event.token_rate === "number" && event.token_rate > 0) {
      setTokenRate(event.token_rate);
    }
  };

  onMount(subscribe);
  onCleanup(() => stop?.());

  const orderedStages = createMemo(() => {
    const seen = order();
    return [...seen].sort((a, b) => {
      const ra = STAGE_RANK[a] ?? 4;
      const rb = STAGE_RANK[b] ?? 4;
      if (ra !== rb) {
        return ra - rb;
      }
      return seen.indexOf(a) - seen.indexOf(b);
    });
  });

  const retry = async () => {
    setActionError("");
    setRetrying(true);
    const finish = appStore.beginOperation();
    try {
      await retrySession(props.sessionId);
      subscribe();
    } catch (error) {
      setActionError(retryErrorMessage(error));
    } finally {
      finish();
      setRetrying(false);
    }
  };

  const rephrase = async () => {
    const draft = await draftFromSession(props.sessionId);
    if (draft) {
      writeStartDraft(draft);
    }
    window.location.hash = routeHref("/start");
  };

  const succeeded = createMemo(() => {
    const r = result();
    return r !== null && (r.status === "ready" || r.status === "complete");
  });
  const failed = createMemo(() => result()?.status === "failed");
  const scopeRejected = createMemo(() => failed() && result()?.failed_stage === "scope_check");

  return (
    <section class="generation-view">
      <header class="view-heading">
        <div>
          <h1>{headingFor(result())}</h1>
          <p>{subtitleFor(result())}</p>
        </div>
        <a class="button-link secondary-link" href={routeHref("/")}>Home</a>
      </header>

      <ol class="stage-list" aria-label="Generation progress">
        <For each={orderedStages()}>
          {(stage) => (
            <StageRow
              stage={stage}
              status={stages[stage]?.status ?? "pending"}
              tokenRate={tokenRate()}
            />
          )}
        </For>
        <Show when={orderedStages().length === 0 && !result() && !connectionError()}>
          <li class="stage-row" data-status="in_progress">
            <span class="stage-icon" aria-hidden="true" />
            <span class="stage-label">Connecting to the generator…</span>
          </li>
        </Show>
      </ol>

      <Show when={actionError()}>
        <p class="form-error" role="alert">{actionError()}</p>
      </Show>

      <Switch>
        <Match when={connectionError() && !result()}>
          <div class="generation-panel" role="alert">
            <h2>Lost the progress connection</h2>
            <p>We couldn't keep the live progress stream open. Generation may still be running.</p>
            <div class="panel-actions">
              <button class="primary-button" type="button" onClick={subscribe}>Reconnect</button>
              <a class="button-link secondary-link" href={routeHref("/")}>Back home</a>
            </div>
          </div>
        </Match>

        <Match when={succeeded()}>
          <div class="generation-panel" data-tone="success">
            <h2>Session ready</h2>
            <p>{readySummary(result())}</p>
            <div class="panel-actions">
              <Show when={result()?.content_type === "phrase_set"}>
                <a class="button-link" href={routeHref(`/phrases/${encodeURIComponent(props.sessionId)}`)}>
                  View phrases
                </a>
              </Show>
              <Show when={result()?.content_type !== "phrase_set" && result()?.story_id}>
                <a class="button-link" href={routeHref(`/reader/${encodeURIComponent(result()?.story_id ?? "")}`)}>
                  Start reading
                </a>
              </Show>
              <Show when={(result()?.tasks?.total ?? 0) > 0}>
                <a class="button-link secondary-link" href={routeHref(`/tasks/${encodeURIComponent(props.sessionId)}`)}>
                  View tasks
                </a>
              </Show>
            </div>
          </div>
        </Match>

        <Match when={scopeRejected()}>
          <div class="generation-panel" data-tone="warning" role="alert">
            <h2>That topic won't work yet</h2>
            <p>{scopeMessage(result())}</p>
            <p class="panel-hint">Try a broader or simpler version of the idea.</p>
            <div class="panel-actions">
              <button class="primary-button" type="button" onClick={() => void rephrase()}>Rephrase</button>
              <a class="button-link secondary-link" href={routeHref("/start")}>Choose another mode</a>
            </div>
            <p class="panel-code">Reference: <code>{result()?.error_code || "scope_check"}</code></p>
          </div>
        </Match>

        <Match when={failed()}>
          <div class="generation-panel" data-tone="error" role="alert">
            <h2>Generation failed</h2>
            <p>{failureMessage(result())}</p>
            <div class="panel-actions">
              <button class="primary-button" type="button" disabled={retrying()} onClick={() => void retry()}>
                {retrying() ? "Retrying…" : "Try again"}
              </button>
              <a class="button-link secondary-link" href={routeHref("/")}>Back home</a>
            </div>
            <p class="panel-code">
              Reference: <code>{result()?.error_code || "unknown"}</code>
              <Show when={result()?.failed_stage}>{` · ${stageLabel(result()?.failed_stage ?? "")}`}</Show>
            </p>
          </div>
        </Match>
      </Switch>
    </section>
  );
}

function StageRow(props: { stage: string; status: StageStatus; tokenRate: number }) {
  const showTicker = () =>
    (props.stage === "story_generation" || props.stage === "phrase_generation") &&
    props.status === "in_progress";
  return (
    <li class="stage-row" data-status={props.status}>
      <span class="stage-icon" aria-hidden="true">{stageGlyph(props.status)}</span>
      <span class="stage-label">{stageLabel(props.stage)}</span>
      <Show when={showTicker()}>
        <TokenTicker rate={props.tokenRate} />
      </Show>
      <span class="stage-status" aria-live="polite">{statusText(props.status)}</span>
    </li>
  );
}

// TokenTicker visualises the upstream tokens/sec rate — explicitly a throughput
// gauge, not a preview of the story text (which the server never streams). The
// bar width eases via a CSS transition as new rates arrive; a shimmer keeps it
// alive between updates. See context/session-types.md ("Generation UX").
function TokenTicker(props: { rate: number }) {
  const fill = () => Math.min(100, Math.round((props.rate / TOKEN_RATE_FULL_SCALE) * 100));
  return (
    <span class="token-ticker" aria-label={`Generating at about ${props.rate} tokens per second`}>
      <span class="token-ticker-bar">
        <span class="token-ticker-fill" style={{ width: `${fill()}%` }} />
      </span>
      <span class="token-ticker-rate">{props.rate > 0 ? `${props.rate} tok/s` : "warming up…"}</span>
    </span>
  );
}

function stageGlyph(status: StageStatus): string {
  switch (status) {
    case "complete":
      return "✓";
    case "failed":
      return "✕";
    default:
      return "";
  }
}

function statusText(status: StageStatus): string {
  switch (status) {
    case "complete":
      return "Done";
    case "failed":
      return "Failed";
    case "in_progress":
      return "Working…";
    default:
      return "Waiting";
  }
}

function stageLabel(stage: string): string {
  if (STAGE_LABELS[stage]) {
    return STAGE_LABELS[stage];
  }
  if (stage.startsWith("task_")) {
    return `Practice: ${humanize(stage.slice("task_".length))}`;
  }
  return humanize(stage);
}

function humanize(value: string): string {
  return value
    .split(/[_\s]+/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function headingFor(result: GenerationEvent | null): string {
  if (!result) {
    return "Generating your session";
  }
  if (result.status === "ready" || result.status === "complete") {
    return "Your session is ready";
  }
  if (result.status === "failed") {
    return "Generation didn't finish";
  }
  return "Generating your session";
}

function subtitleFor(result: GenerationEvent | null): string {
  if (!result) {
    return "Each step is checkpointed — a failed step can be retried on its own.";
  }
  if (result.status === "ready" || result.status === "complete") {
    return "Story and at least one task type are ready.";
  }
  return "You can retry from the step that failed.";
}

function readySummary(result: GenerationEvent | null): string {
  const total = result?.tasks?.total ?? 0;
  const noun = result?.content_type === "phrase_set" ? "phrase set" : "story";
  if (total > 0) {
    return `Your ${noun} is ready, with ${total} practice ${total === 1 ? "task" : "tasks"}.`;
  }
  return result?.content_type === "phrase_set"
    ? "Your phrase set is ready."
    : "Your story is ready to read.";
}

function failureMessage(result: GenerationEvent | null): string {
  // The server's human-readable detail is the most specific; fall back to the
  // per-code copy, then a generic line.
  if (result?.error_detail) {
    return result.error_detail;
  }
  const code = result?.error_code;
  if (code && ERROR_MESSAGES[code]) {
    return ERROR_MESSAGES[code];
  }
  return "Something went wrong while generating this session.";
}

function scopeMessage(result: GenerationEvent | null): string {
  // The scope check returns a topic-specific reason (and an optional rephrasing);
  // prefer it over generic copy so the learner knows exactly why it was rejected.
  if (result?.error_detail) {
    return result.error_detail;
  }
  const code = result?.error_code;
  if (code && ERROR_MESSAGES[code]) {
    return ERROR_MESSAGES[code];
  }
  return "This topic is too specialized to turn into a readable story at your current level.";
}

async function draftFromSession(sessionID: string): Promise<{
  mode: SessionMode;
  topic?: string;
  expressions?: string[];
  expressionOutput?: "phrases" | "story";
} | null> {
  try {
    const detail = await getSessionDetail(sessionID);
    return {
      mode: detail.session_type,
      topic: detail.topic,
      expressions: detail.user_expressions,
      expressionOutput: detail.expression_output,
    };
  } catch {
    return null;
  }
}

function retryErrorMessage(error: unknown): string {
  if (error instanceof APIError && error.status === 503) {
    return "Generation is not configured. Start the gateway before retrying.";
  }
  return "The session could not be retried.";
}
