import { createMemo, createSignal, For, Match, onMount, Show, Switch, type Accessor, type JSX } from "solid-js";
import { createStore, type SetStoreFunction } from "solid-js/store";
import { APIError, getSessionTasks, getTask, submitTask, type APIRequest, type APISchema } from "../api";
import { routeHref } from "../router";
import { appStore } from "../store";

type Task = APISchema<"Task">;
type Grade = APISchema<"Grade">;
type SkillXPDelta = APISchema<"SkillXPDelta">;
type SubmitRequest = APIRequest<"submitTask">;
type ResponseStore = Record<string, unknown>;

// Task types whose grading routes through the LLM gateway: their submit is slow
// and can come back 503 (no gateway) / 502 (gateway error), so the UI shows an
// explicit pending state for them rather than the instant flip rule types get.
const LLM_TYPES = new Set(["production"]);

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

export function TasksView(props: { sessionId: string }) {
  const [status, setStatus] = createSignal<"loading" | "ready" | "error">("loading");
  const [tasks, setTasks] = createStore<Task[]>([]);

  const total = createMemo(() => tasks.length);
  const completed = createMemo(() => tasks.filter((task) => task.graded).length);
  const allDone = createMemo(() => total() > 0 && completed() === total());

  onMount(() => void load());

  async function load() {
    setStatus("loading");
    const finish = appStore.beginOperation();
    try {
      const data = await getSessionTasks(props.sessionId);
      setTasks(data.tasks);
      setStatus("ready");
    } catch {
      setStatus("error");
    } finally {
      finish();
    }
  }

  // Submit a card's response, persist the resulting grade into the store, and
  // announce it. Re-submitting an already-graded task is a 409 (resubmission to
  // improve a grade is future work — #51); we then fetch the authoritative grade
  // so the card still settles into its graded state. Any other failure is thrown
  // back to the card, which keeps the task submittable and shows a message.
  async function submit(index: number, request: SubmitRequest): Promise<void> {
    const task = tasks[index];
    try {
      const result = await submitTask(task.task_id, request);
      setTasks(index, "grade", result.grade);
      setTasks(index, "graded", true);
      announceGrade(result.grade, result.skill_xp);
    } catch (error) {
      if (error instanceof APIError && error.status === 409) {
        const fresh = await getTask(task.task_id);
        setTasks(index, "grade", fresh.grade);
        setTasks(index, "graded", true);
        return;
      }
      throw error;
    }
  }

  return (
    <section class="tasks-view">
      <header class="view-heading">
        <div>
          <h1>Tasks</h1>
          <p>{progressLabel(completed(), total(), status())}</p>
        </div>
        <a class="button-link secondary-link" href={routeHref("/")}>Back home</a>
      </header>

      <Switch>
        <Match when={status() === "loading"}>
          <div class="tasks-state" aria-busy="true">Loading tasks...</div>
        </Match>
        <Match when={status() === "error"}>
          <div class="tasks-state" role="alert">
            <p>These tasks could not be loaded.</p>
            <button class="secondary-button" type="button" onClick={() => void load()}>Retry</button>
          </div>
        </Match>
        <Match when={total() === 0}>
          <div class="tasks-state empty-state">
            <h2>No tasks yet</h2>
            <p>This session has no tasks attached. Read the story or start a new session.</p>
          </div>
        </Match>
        <Match when={total() > 0}>
          <div
            class="task-progress"
            role="progressbar"
            aria-valuemin={0}
            aria-valuemax={total()}
            aria-valuenow={completed()}
            aria-label="Tasks completed"
          >
            <div class="task-progress-bar" style={{ width: `${(completed() / total()) * 100}%` }} />
          </div>
          <Show when={allDone()}>
            <p class="tasks-complete" role="status">All tasks complete for this session.</p>
          </Show>
          <ol class="task-list">
            <For each={tasks}>
              {(task, index) => (
                <li>
                  <TaskCard task={task} position={index() + 1} onSubmit={(request) => submit(index(), request)} />
                </li>
              )}
            </For>
          </ol>
        </Match>
      </Switch>
    </section>
  );
}

function TaskCard(props: { task: Task; position: number; onSubmit: (request: SubmitRequest) => Promise<void> }) {
  const [response, setResponse] = createStore<ResponseStore>({});
  const [submitting, setSubmitting] = createSignal(false);
  const [errorMessage, setErrorMessage] = createSignal("");

  const renderer = (): Renderer | undefined => RENDERERS[props.task.task_type];
  const needsLLM = () => LLM_TYPES.has(props.task.task_type);
  const canSubmit = () => {
    const current = renderer();
    return current ? current.canSubmit(response) : false;
  };

  async function handleSubmit(event: Event) {
    event.preventDefault();
    if (!canSubmit() || submitting()) {
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
        <span class="task-type-chip">{rendererLabel(props.task.task_type)}</span>
      </header>

      <Show
        when={renderer()}
        fallback={
          <p class="task-unsupported">
            This task type (<code>{props.task.task_type}</code>) isn't supported by this client yet.
          </p>
        }
      >
        {(current) => (
          <Show
            when={!props.task.graded}
            fallback={<GradeView grade={props.task.grade} />}
          >
            <form class="task-form" onSubmit={handleSubmit}>
              {current().body({
                content: props.task.content,
                response,
                setResponse,
                disabled: submitting,
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
                  {submitting() ? "Submitting..." : "Submit"}
                </button>
              </div>
            </form>
          </Show>
        )}
      </Show>
    </article>
  );
}

function GradeView(props: { grade?: Grade }) {
  return (
    <Show when={props.grade}>
      {(grade) => (
        <div class="task-grade" data-correct={grade().correct ? "" : undefined}>
          <div class="task-grade-head">
            <span class="task-grade-status">{grade().correct ? "Correct" : "Not quite"}</span>
            <span class="task-grade-by">{grade().graded_by === "llm" ? "AI-graded" : "Auto-graded"}</span>
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

function announceGrade(grade: Grade, skillXP: SkillXPDelta[]) {
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
