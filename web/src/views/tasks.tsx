import { createMemo, createSignal, For, Match, onCleanup, onMount, Show, Switch, type Accessor, type JSX } from "solid-js";
import { createStore, type SetStoreFunction } from "solid-js/store";
import { APIError, getSessionTasks, getTask, reportTask, submitTask, type APIRequest, type APISchema } from "../api";
import { routeHref } from "../router";
import { appStore } from "../store";

export type Task = APISchema<"Task"> & { attempt_count?: number };
type Grade = APISchema<"Grade">;
type SkillXPDelta = APISchema<"SkillXPDelta">;
type SubmitRequest = APIRequest<"submitTask">;
export type ReportRequest = APIRequest<"reportTask">;
type ReportReason = APISchema<"TaskReportReason">;
type ReportState = APISchema<"TaskReportState">;
export type ReportResponse = APISchema<"TaskReportResponse">;
type ResponseStore = Record<string, unknown>;
export type TaskLoadStatus = "loading" | "ready" | "error";

// Task types whose grading routes through the LLM gateway: their submit is slow
// and can come back 503 (no gateway) / 502 (gateway error), so the UI shows an
// explicit pending state for them rather than the instant flip rule types get.
const LLM_TYPES = new Set(["production"]);
const REPORT_POLL_STATUSES = new Set(["queued", "regenerating"]);
const REPORT_REASONS: { value: ReportReason; label: string }[] = [
  { value: "malformed", label: "Malformed" },
  { value: "wrong_answer_key", label: "Wrong key" },
  { value: "nonsensical", label: "Nonsensical" },
  { value: "too_hard", label: "Too hard" },
];

// ---- per-type renderer registry --------------------------------------------
// One entry per server TaskType. Adding a new task type on the server needs only
// a new entry here — the load/submit/grade machinery below is type-agnostic. The
// presented `content` is answer-free (the server strips correct_index /
// acceptable_forms), so a renderer only ever sees what the learner may see.

interface RendererProps {
  content: Record<string, unknown>;
  response: ResponseStore;
  setResponse: SetStoreFunction<ResponseStore>;
  disabled: Accessor<boolean>;
  // Stable group name for radio inputs (unique per task).
  name: string;
  // Target language, for `lang` on target-language text; "" when unknown.
  lang: string;
}

interface Renderer {
  label: string;
  body: (props: RendererProps) => JSX.Element;
  canSubmit: (response: ResponseStore) => boolean;
}

const RENDERERS: Record<string, Renderer> = {
  comprehension_mc: {
    label: "Comprehension",
    body: (p) => {
      const question = asString(p.content, "question");
      const options = asStringArray(p.content, "options");
      return (
        <fieldset class="task-mc">
          <legend class="task-question" lang={p.lang || undefined}>{question}</legend>
          <For each={options}>
            {(option, index) => (
              <label class="task-option">
                <input
                  type="radio"
                  name={p.name}
                  checked={p.response.selected_index === index()}
                  disabled={p.disabled()}
                  onChange={() => p.setResponse("selected_index", index())}
                />
                <span lang={p.lang || undefined}>{option}</span>
              </label>
            )}
          </For>
        </fieldset>
      );
    },
    canSubmit: (r) => typeof r.selected_index === "number",
  },
  fill_blank: {
    label: "Fill in the blank",
    body: (p) => {
      const sentence = asString(p.content, "sentence");
      return (
        <div class="task-fill">
          <p class="task-sentence" lang={p.lang || undefined}>{sentence}</p>
          <input
            class="task-text-input"
            type="text"
            autocomplete="off"
            spellcheck={false}
            placeholder="Your answer"
            lang={p.lang || undefined}
            value={asString(p.response, "answer")}
            disabled={p.disabled()}
            onInput={(event) => p.setResponse("answer", event.currentTarget.value)}
          />
        </div>
      );
    },
    canSubmit: (r) => typeof r.answer === "string" && r.answer.trim() !== "",
  },
  production: {
    label: "Production",
    body: (p) => {
      // prompt_l1 is the idea to express, in the learner's first language — so it
      // carries no target-language `lang`. The response the learner writes is in
      // the target language.
      const prompt = asString(p.content, "prompt_l1");
      return (
        <div class="task-production">
          <p class="task-prompt">{prompt}</p>
          <textarea
            class="task-textarea"
            rows={3}
            placeholder="Write your response in the target language"
            lang={p.lang || undefined}
            value={asString(p.response, "text")}
            disabled={p.disabled()}
            onInput={(event) => p.setResponse("text", event.currentTarget.value)}
          />
        </div>
      );
    },
    canSubmit: (r) => typeof r.text === "string" && r.text.trim() !== "",
  },
};

// Shared report-and-poll wiring for any owner of a Task list store (TasksView
// and the session shell's task panel): submits the report, refreshes the task
// row, and polls queued/regenerating reports until they settle. The owner must
// call dispose() on cleanup to stop in-flight polling loops.
export function createTaskReportController(owner: {
  tasks: () => readonly Task[];
  setTask: (index: number, task: Task) => void;
  setReport: (index: number, report: ReportState) => void;
}) {
  const polling = new Set<string>();
  let disposed = false;

  async function report(index: number, request: ReportRequest): Promise<ReportResponse> {
    const task = owner.tasks()[index];
    const result = await reportTask(task.task_id, request);
    try {
      const fresh = await getTask(task.task_id);
      owner.setTask(index, fresh);
      if (isPollingReport(fresh.report)) {
        void poll(task.task_id);
      }
    } catch {
      const fallback = reportFallbackState(result, request.reason);
      owner.setReport(index, fallback);
      if (isPollingReport(fallback)) {
        void poll(task.task_id);
      }
    }
    return result;
  }

  function pollAll(nextTasks: readonly Task[]) {
    nextTasks.forEach((task) => {
      if (isPollingReport(task.report)) {
        void poll(task.task_id);
      }
    });
  }

  async function poll(taskID: string) {
    if (polling.has(taskID)) {
      return;
    }
    polling.add(taskID);
    try {
      while (!disposed) {
        await delay(2000);
        const index = owner.tasks().findIndex((task) => task.task_id === taskID);
        if (disposed || index < 0 || !isPollingReport(owner.tasks()[index]?.report)) {
          return;
        }
        try {
          const fresh = await getTask(taskID);
          owner.setTask(index, fresh);
          if (!isPollingReport(fresh.report)) {
            return;
          }
        } catch {
          await delay(5000);
        }
      }
    } finally {
      polling.delete(taskID);
    }
  }

  function dispose() {
    disposed = true;
    polling.clear();
  }

  return { report, pollAll, dispose };
}

export function TasksView(props: { sessionId: string }) {
  const [status, setStatus] = createSignal<TaskLoadStatus>("loading");
  const [tasks, setTasks] = createStore<Task[]>([]);
  const reports = createTaskReportController({
    tasks: () => tasks,
    setTask: (index, task) => setTasks(index, task),
    setReport: (index, report) => setTasks(index, "report", report),
  });

  const total = createMemo(() => tasks.length);
  const completed = createMemo(() => tasks.filter((task) => task.graded).length);

  onMount(() => void load());
  onCleanup(() => reports.dispose());

  async function load() {
    setStatus("loading");
    const finish = appStore.beginOperation();
    try {
      const data = await getSessionTasks(props.sessionId);
      setTasks(data.tasks);
      reports.pollAll(data.tasks);
      setStatus("ready");
    } catch {
      setStatus("error");
    } finally {
      finish();
    }
  }

  // Submit a card's response, persist the resulting grade and attempt count into
  // the store, and announce it. The backend accepts re-submissions and applies
  // best-grade-wins semantics to learning signals.
  async function submit(index: number, request: SubmitRequest): Promise<void> {
    const task = tasks[index];
    const result = await submitTask(task.task_id, request);
    setTasks(index, "grade", result.grade);
    setTasks(index, "graded", true);
    setTasks(index, "attempt_count", result.attempt_count);
    announceGrade(result.grade, result.skill_xp);
  }

  return (
    <TasksPanel
      status={status()}
      tasks={tasks}
      total={total()}
      completed={completed()}
      showHeading
      actions={<a class="button-link secondary-link" href={routeHref("/")}>Back home</a>}
      onRetry={() => void load()}
      onSubmit={(index, request) => submit(index, request)}
      onReport={(index, request) => reports.report(index, request)}
    />
  );
}

export function TasksPanel(props: {
  status: TaskLoadStatus;
  tasks: readonly Task[];
  total: number;
  completed: number;
  showHeading?: boolean;
  actions?: JSX.Element;
  completeAction?: JSX.Element;
  referenceAssisted?: (task: Task) => boolean;
  onShowReference?: () => void;
  onReadAgain?: () => void;
  onRetry?: () => void;
  onSubmit: (index: number, request: SubmitRequest) => Promise<void>;
  onReport: (index: number, request: ReportRequest) => Promise<ReportResponse>;
}) {
  const allDone = createMemo(() => props.total > 0 && props.completed === props.total);

  return (
    <section class="tasks-view">
      <Show when={props.showHeading}>
        <header class="view-heading">
          <div>
            <h1>Tasks</h1>
            <p>{progressLabel(props.completed, props.total, props.status)}</p>
          </div>
          <Show when={props.actions}>
            <div class="view-heading-actions">{props.actions}</div>
          </Show>
        </header>
      </Show>

      <Switch>
        <Match when={props.status === "loading"}>
          <div class="tasks-state" aria-busy="true">Loading tasks...</div>
        </Match>
        <Match when={props.status === "error"}>
          <div class="tasks-state" role="alert">
            <p>These tasks could not be loaded.</p>
            <Show when={props.onRetry}>
              {(retry) => <button class="secondary-button" type="button" onClick={retry()}>Retry</button>}
            </Show>
          </div>
        </Match>
        <Match when={props.total === 0}>
          <div class="tasks-state empty-state">
            <h2>No tasks in this session</h2>
            <p>Read the session content or start a new session.</p>
            <Show when={props.completeAction}>{props.completeAction}</Show>
          </div>
        </Match>
        <Match when={props.total > 0}>
          <div
            class="task-progress"
            role="progressbar"
            aria-valuemin={0}
            aria-valuemax={props.total}
            aria-valuenow={props.completed}
            aria-label="Tasks completed"
          >
            <div class="task-progress-bar" style={{ width: `${(props.completed / props.total) * 100}%` }} />
          </div>
          <Show when={allDone()}>
            <p class="tasks-complete" role="status">All tasks have a current grade for this session.</p>
          </Show>
          <Show when={props.completed < props.total && (props.onShowReference || props.onReadAgain)}>
            <div class="task-reference-actions">
              <Show when={props.onShowReference}>
                {(showReference) => (
                  <button class="secondary-button" type="button" onClick={showReference()}>
                    Show story
                  </button>
                )}
              </Show>
              <Show when={props.completed === 0 && props.onReadAgain}>
                {(readAgain) => (
                  <button class="secondary-button" type="button" onClick={readAgain()}>
                    Not ready - read again
                  </button>
                )}
              </Show>
            </div>
          </Show>
          <ol class="task-list">
            <For each={props.tasks}>
              {(task, index) => (
                <li>
                  <TaskCard
                    task={task}
                    position={index() + 1}
                    referenceAssisted={props.referenceAssisted?.(task) ?? task.reference_assisted}
                    onSubmit={(request) => props.onSubmit(index(), request)}
                    onReport={(request) => props.onReport(index(), request)}
                  />
                </li>
              )}
            </For>
          </ol>
          <Show when={props.completeAction}>
            <div class="task-panel-actions">{props.completeAction}</div>
          </Show>
        </Match>
      </Switch>
    </section>
  );
}

function TaskCard(props: {
  task: Task;
  position: number;
  referenceAssisted: boolean;
  onSubmit: (request: SubmitRequest) => Promise<void>;
  onReport: (request: ReportRequest) => Promise<ReportResponse>;
}) {
  const [response, setResponse] = createStore<ResponseStore>({});
  const [submitting, setSubmitting] = createSignal(false);
  const [errorMessage, setErrorMessage] = createSignal("");

  const renderer = (): Renderer | undefined => RENDERERS[props.task.task_type];
  const needsLLM = () => LLM_TYPES.has(props.task.task_type);
  const regenerating = () => isPollingReport(props.task.report);
  const disabled = () => submitting() || regenerating();
  const canSubmit = () => {
    const current = renderer();
    return current ? current.canSubmit(response) && !regenerating() : false;
  };

  async function handleSubmit(event: Event) {
    event.preventDefault();
    if (!canSubmit() || disabled()) {
      return;
    }
    setErrorMessage("");
    setSubmitting(true);
    try {
      // input_method is a property of the response; only "typed" exists today.
      // Scan (OCR) and audio (STT) plug in here later without touching renderers (#22).
      await props.onSubmit({ response: { ...response }, input_method: "typed" });
    } catch (error) {
      setErrorMessage(submitErrorMessage(error));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <article class="task-card" data-type={props.task.task_type} data-graded={props.task.graded ? "" : undefined}>
      <header class="task-card-head">
        <span class="task-number">Task {props.position}</span>
        <span class="task-card-tags">
          <Show when={props.referenceAssisted}>
            <span class="task-reference-chip">Story viewed</span>
          </Show>
          <span class="task-type-chip">{rendererLabel(props.task.task_type)}</span>
        </span>
      </header>
      <ReportStatusView report={props.task.report} />

      <Show
        when={renderer()}
        fallback={
          <p class="task-unsupported">
            This task type (<code>{props.task.task_type}</code>) isn't supported by this client yet.
          </p>
        }
      >
        {(current) => (
          <>
            <Show when={props.task.graded}>
              <GradeView grade={props.task.grade} attemptCount={props.task.attempt_count} />
            </Show>
            <form class="task-form" onSubmit={handleSubmit}>
              <Show when={props.task.graded}>
                <p class="task-resubmit-note">Try again to replace this task's current grade if you improve.</p>
              </Show>
              {current().body({
                content: props.task.content,
                response,
                setResponse,
                disabled,
                name: `task-${props.task.task_id}`,
                lang: appStore.activeLanguage(),
              })}

              <Show when={submitting() && needsLLM()}>
                <p class="task-pending" role="status" aria-busy="true">Grading your response with AI...</p>
              </Show>
              <Show when={errorMessage()}>
                <p class="task-error" role="alert">{errorMessage()}</p>
              </Show>

              <div class="task-actions">
                <button class="primary-button" type="submit" disabled={!canSubmit() || submitting()}>
                  {submitting() ? "Submitting..." : props.task.graded ? "Try again" : "Submit"}
                </button>
              </div>
            </form>
            <ReportControl disabled={disabled()} onReport={props.onReport} />
          </>
        )}
      </Show>
    </article>
  );
}

function ReportControl(props: { disabled: boolean; onReport: (request: ReportRequest) => Promise<ReportResponse> }) {
  const [open, setOpen] = createSignal(false);
  const [reason, setReason] = createSignal<ReportReason | "">("");
  const [note, setNote] = createSignal("");
  const [submitting, setSubmitting] = createSignal(false);
  const [message, setMessage] = createSignal("");
  const [error, setError] = createSignal("");

  async function submitReport(event: Event) {
    event.preventDefault();
    if (!reason() || submitting()) {
      return;
    }
    setSubmitting(true);
    setError("");
    setMessage("");
    try {
      const result = await props.onReport({ reason: reason() as ReportReason, note: note().trim() || undefined });
      setMessage(result.message);
      setOpen(false);
      setReason("");
      setNote("");
    } catch (err) {
      setError(reportErrorMessage(err));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div class="task-report-control">
      <Show
        when={open()}
        fallback={
          <button class="task-report-toggle" type="button" disabled={props.disabled} onClick={() => setOpen(true)}>
            Report a problem
          </button>
        }
      >
        <form class="task-report-form" onSubmit={submitReport}>
          <div class="task-report-reasons" role="group" aria-label="Report reason">
            <For each={REPORT_REASONS}>
              {(option) => (
                <button
                  class="task-report-chip"
                  type="button"
                  aria-pressed={reason() === option.value}
                  disabled={submitting()}
                  onClick={() => setReason(option.value)}
                >
                  {option.label}
                </button>
              )}
            </For>
          </div>
          <Show when={reason()}>
            <textarea
              class="task-report-note"
              rows={2}
              maxLength={1000}
              placeholder="Optional note"
              value={note()}
              disabled={submitting()}
              onInput={(event) => setNote(event.currentTarget.value)}
            />
            <div class="task-report-actions">
              <button class="secondary-button" type="button" disabled={submitting()} onClick={() => setOpen(false)}>
                Cancel
              </button>
              <button class="primary-button" type="submit" disabled={submitting()}>
                {submitting() ? "Sending..." : "Send report"}
              </button>
            </div>
          </Show>
        </form>
      </Show>
      <Show when={message()}>
        <p class="task-report-message" role="status">{message()}</p>
      </Show>
      <Show when={error()}>
        <p class="task-error" role="alert">{error()}</p>
      </Show>
    </div>
  );
}

function ReportStatusView(props: { report?: ReportState }) {
  return (
    <Show when={props.report}>
      {(report) => (
        <p class="task-report-status" data-status={report().status} role="status" aria-busy={isPollingReport(report()) ? "true" : undefined}>
          {report().message}
        </p>
      )}
    </Show>
  );
}

function GradeView(props: { grade?: Grade; attemptCount?: number }) {
  return (
    <Show when={props.grade}>
      {(grade) => (
        <div class="task-grade" data-correct={grade().correct ? "" : undefined}>
          <div class="task-grade-head">
            <div>
              <span class="task-grade-label">Current grade</span>
              <span class="task-grade-status">{grade().correct ? "Correct" : "Not quite"}</span>
            </div>
            <div class="task-grade-meta">
              <Show when={props.attemptCount}>
                {(attemptCount) => (
                  <span>{attemptCount()} attempt{attemptCount() === 1 ? "" : "s"}</span>
                )}
              </Show>
              <span>{grade().graded_by === "llm" ? "AI-graded" : "Auto-graded"}</span>
            </div>
          </div>
          <Show when={grade().feedback}>
            <p class="task-grade-feedback">{grade().feedback}</p>
          </Show>
          <Show when={(grade().items_demonstrated?.length ?? 0) > 0}>
            <p class="task-grade-items">
              Demonstrated {grade().items_demonstrated!.length} knowledge item
              {grade().items_demonstrated!.length === 1 ? "" : "s"}.
            </p>
          </Show>
        </div>
      )}
    </Show>
  );
}

export function announceGrade(grade: Grade, skillXP: SkillXPDelta[]) {
  const xpDelta = skillXP.reduce((sum, change) => sum + change.xp_delta, 0);
  const pending = skillXP.filter((change) => change.pending_verify).length;
  if (xpDelta !== 0) {
    const sign = xpDelta > 0 ? "+" : "";
    const pendingText = pending > 0 ? ` ${pending} skill${pending === 1 ? "" : "s"} pending verification.` : "";
    appStore.showToast(`${sign}${xpDelta} skill XP.${pendingText}`, "info");
    return;
  }
  if (grade.correct) {
    const items = grade.items_demonstrated?.length ?? 0;
    appStore.showToast(
      items > 0 ? `Correct — ${items} item${items === 1 ? "" : "s"} demonstrated.` : "Correct!",
      "info",
    );
    return;
  }
  appStore.showToast("Not quite — check the feedback on the task.", "info");
}

function progressLabel(completed: number, total: number, status: "loading" | "ready" | "error"): string {
  if (status !== "ready" || total === 0) {
    return "Complete the tasks for this session.";
  }
  return `${completed} of ${total} done`;
}

function rendererLabel(taskType: string): string {
  return RENDERERS[taskType]?.label ?? taskType;
}

function submitErrorMessage(error: unknown): string {
  if (error instanceof APIError) {
    if (error.status === 503) {
      return "AI grading isn't configured on this server yet. Try again once a gateway is available.";
    }
    if (error.status === 502) {
      return "The AI grader is unavailable right now. Please try again.";
    }
    if (error.status === 500) {
      return "This task has a problem and can't be graded. Try generating a new session.";
    }
    if (error.status === 400) {
      return "That response wasn't accepted. Check your answer and try again.";
    }
    if (error.status === 404) {
      return "This task no longer exists.";
    }
  }
  return "Your response could not be submitted.";
}

function isPollingReport(report?: ReportState): boolean {
  return report ? REPORT_POLL_STATUSES.has(report.status) : false;
}

function reportFallbackState(response: ReportResponse, reason: ReportReason): ReportState {
  return {
    report_id: response.report_id,
    status: response.status,
    reason,
    message: response.message,
    replacement_task_id: response.replacement_task_id,
    reported_at: Date.now() / 1000,
    regeneration_cap: response.regeneration_cap,
    regenerations_used: response.regenerations_used,
  };
}

function reportErrorMessage(error: unknown): string {
  if (error instanceof APIError) {
    if (error.status === 400) {
      return "Choose a report reason before sending.";
    }
    if (error.status === 404) {
      return "This task no longer exists.";
    }
  }
  return "The report could not be sent.";
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

// ---- tolerant content/response accessors ------------------------------------
// content and response are opaque JSON owned by the server task type, so we read
// them defensively rather than trusting a fixed shape (mirrors the Go side's
// content helpers).

function asString(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  return typeof value === "string" ? value : "";
}

function asStringArray(source: Record<string, unknown>, key: string): string[] {
  const value = source[key];
  if (!Array.isArray(value)) {
    return [];
  }
  return value.filter((item): item is string => typeof item === "string");
}
