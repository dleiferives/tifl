import { createMemo, createSignal, For, Match, onMount, Show, Switch } from "solid-js";
import { APIError, getSessionDebug, type APISchema } from "../api";
import { routeHref, sessionHref } from "../router";
import { appStore } from "../store";

type SessionDebug = APISchema<"SessionDebug">;
type LLMCall = APISchema<"LLMCall">;

const dateFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "medium",
});

export function DebugView(props: { sessionId: string }) {
  const [debug, setDebug] = createSignal<SessionDebug | null>(null);
  const [loading, setLoading] = createSignal(true);
  const [error, setError] = createSignal("");

  const load = async () => {
    setLoading(true);
    setError("");
    const finish = appStore.beginOperation();
    try {
      setDebug(await getSessionDebug(props.sessionId));
    } catch (err) {
      setError(debugErrorMessage(err));
    } finally {
      finish();
      setLoading(false);
    }
  };

  onMount(() => {
    void load();
  });

  const calls = createMemo(() => debug()?.llm_calls ?? []);
  const totalTokens = createMemo(() => calls().reduce((sum, call) => sum + (call.input_tokens ?? 0) + (call.output_tokens ?? 0), 0));
  const failedCalls = createMemo(() => calls().filter((call) => call.status !== "success").length);

  return (
    <section class="debug-view">
      <header class="view-heading">
        <div>
          <h1>Session debug</h1>
          <p><code>{props.sessionId}</code></p>
        </div>
        <div class="view-heading-actions">
          <a class="button-link secondary-link" href={sessionHref(props.sessionId, "read")}>Session</a>
          <a class="button-link secondary-link" href={routeHref("/")}>Home</a>
        </div>
      </header>

      <Switch>
        <Match when={loading()}>
          <div class="home-state" aria-busy="true">Loading debug data...</div>
        </Match>
        <Match when={error()}>
          <div class="home-state" role="alert">
            <p>{error()}</p>
            <button class="secondary-button" type="button" onClick={() => void load()}>Retry</button>
          </div>
        </Match>
        <Match when={debug()}>
          {(data) => (
            <>
              <div class="debug-summary" aria-label="Debug summary">
                <Metric label="Status" value={data().session.status} />
                <Metric label="Stages" value={`${data().session.stage_summary.complete}/${data().session.stage_summary.total}`} />
                <Metric label="LLM calls" value={calls().length} />
                <Metric label="Failed calls" value={failedCalls()} />
                <Metric label="Tokens" value={totalTokens()} />
              </div>

              <section class="debug-section" aria-labelledby="debug-stages">
                <h2 id="debug-stages">Stage timeline</h2>
                <ol class="debug-timeline">
                  <For each={data().session.stages}>
                    {(stage) => (
                      <li class="debug-stage-row" data-status={stage.status}>
                        <div>
                          <strong>{stage.stage}</strong>
                          <span>{stage.status}</span>
                        </div>
                        <dl>
                          <DebugField label="Started" value={formatOptionalTime(stage.started_at)} />
                          <DebugField label="Completed" value={formatOptionalTime(stage.completed_at)} />
                          <DebugField label="Retries" value={String(stage.retry_count)} />
                          <Show when={stage.error_code}>
                            {(code) => <DebugField label="Error" value={code()} />}
                          </Show>
                        </dl>
                        <Show when={stage.error_detail}>
                          {(detail) => <p>{detail()}</p>}
                        </Show>
                      </li>
                    )}
                  </For>
                </ol>
              </section>

              <section class="debug-section" aria-labelledby="debug-calls">
                <h2 id="debug-calls">LLM calls</h2>
                <Show
                  when={calls().length > 0}
                  fallback={<p class="debug-empty">No LLM calls are recorded for this session and user.</p>}
                >
                  <div class="debug-call-list">
                    <For each={calls()}>
                      {(call) => <LLMCallRow call={call} />}
                    </For>
                  </div>
                </Show>
              </section>
            </>
          )}
        </Match>
      </Switch>
    </section>
  );
}

function Metric(props: { label: string; value: number | string }) {
  return (
    <div class="home-metric">
      <span>{props.label}</span>
      <strong>{props.value}</strong>
    </div>
  );
}

function LLMCallRow(props: { call: LLMCall }) {
  const call = props.call;
  const payloads = [
    { label: "System prompt", value: call.system_prompt },
    { label: "User prompt", value: call.user_prompt },
    { label: "Raw response", value: call.raw_response },
    { label: "Parsed output", value: call.parsed_output },
    { label: "Error payload", value: call.error_payload },
  ].filter((entry): entry is { label: string; value: string } => typeof entry.value === "string" && entry.value.length > 0);
  return (
    <article class="debug-call-row" data-status={call.status}>
      <header>
        <div>
          <h3>{call.kind}</h3>
          <p><code>{call.call_id}</code></p>
        </div>
        <span class="status-chip" data-status={call.status}>{call.status}</span>
      </header>
      <dl>
        <DebugField label="Called" value={formatOptionalTime(call.called_at)} />
        <DebugField label="Model" value={call.model} />
        <DebugField label="Prompt" value={call.prompt_version} />
        <DebugField label="Input" value={formatOptionalNumber(call.input_tokens)} />
        <DebugField label="Output" value={formatOptionalNumber(call.output_tokens)} />
        <DebugField label="Latency" value={formatLatency(call.latency_ms)} />
      </dl>
      <Show when={call.error_detail}>
        {(detail) => <p>{detail()}</p>}
      </Show>
      <Show when={payloads.length > 0}>
        <div class="debug-payloads">
          <For each={payloads}>
            {(payload) => (
              <details>
                <summary>{payload.label}</summary>
                <pre>{payload.value}</pre>
              </details>
            )}
          </For>
        </div>
      </Show>
    </article>
  );
}

function DebugField(props: { label: string; value: string }) {
  return (
    <div>
      <dt>{props.label}</dt>
      <dd>{props.value}</dd>
    </div>
  );
}

function formatOptionalTime(value: number | undefined): string {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return "not set";
  }
  return dateFormatter.format(new Date(value * 1000));
}

function formatOptionalNumber(value: number | undefined): string {
  return typeof value === "number" ? String(value) : "n/a";
}

function formatLatency(value: number | undefined): string {
  return typeof value === "number" ? `${value} ms` : "n/a";
}

function debugErrorMessage(error: unknown): string {
  if (error instanceof APIError) {
    if (error.status === 404) {
      return "Session debug data was not found.";
    }
    if (error.status === 401) {
      return "Sign in again to load debug data.";
    }
  }
  return "Debug data could not be loaded.";
}
