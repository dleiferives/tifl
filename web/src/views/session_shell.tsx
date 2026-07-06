import { batch, createEffect, createMemo, createSignal, For, Match, onCleanup, Show, Switch, type JSX } from "solid-js";
import { createStore, reconcile } from "solid-js/store";
import {
  APIError,
  completeSession,
  getSessionContent,
  getSessionDetail,
  getSessionTasks,
  getStory,
  startReading,
  submitTask,
  type APIRequest,
  type APIResponse,
  type APISchema,
} from "../api";
import { routeHref, sessionHref, type SessionStep } from "../router";
import { appStore } from "../store";
import { GenerationView } from "./generation";
import { PhrasesPanel, type SessionContent } from "./phrases";
import { ReaderView } from "./reader";
import { formatLevel, formatUnixSeconds, sessionTitle } from "./session_rows";
import { writeStartDraft } from "./start";
import { announceGrade, TasksPanel, type Task, type TaskLoadStatus } from "./tasks";

type SessionDetail = APIResponse<"getSessionDetail", 200>;
type StoryLoad = APISchema<"StoryLoad">;
type SubmitTaskRequest = APIRequest<"submitTask">;
type LoadStatus = "idle" | "loading" | "ready" | "error";

interface ShellState {
  detail: SessionDetail | null;
  content: SessionContent | null;
  story: StoryLoad | null;
  tasks: Task[];
  detailStatus: LoadStatus;
  contentStatus: LoadStatus;
  storyStatus: LoadStatus;
  tasksStatus: TaskLoadStatus;
  errorMessage: string;
}

const STEPS: { id: SessionStep; label: string }[] = [
  { id: "read", label: "Read" },
  { id: "tasks", label: "Tasks" },
  { id: "review", label: "Review" },
];

export function SessionShellView(props: { sessionId: string; step: SessionStep }) {
  const [state, setState] = createStore<ShellState>({
    detail: null,
    content: null,
    story: null,
    tasks: [],
    detailStatus: "loading",
    contentStatus: "idle",
    storyStatus: "idle",
    tasksStatus: "loading",
    errorMessage: "",
  });
  const [visited, setVisited] = createStore<Record<SessionStep, boolean>>({
    read: false,
    tasks: false,
    review: false,
  });
  const [completing, setCompleting] = createSignal(false);
  const [actionError, setActionError] = createSignal("");

  let loadSeq = 0;
  let loadAbort: AbortController | undefined;
  let activeSessionID = "";
  let lastSwitchEvent = "";
  let readingStartPending = false;

  onCleanup(() => loadAbort?.abort());

  createEffect(() => {
    const sessionID = props.sessionId;
    if (sessionID !== activeSessionID) {
      activeSessionID = sessionID;
      resetVisited(props.step);
      void loadSession(sessionID);
      return;
    }
    setVisited(props.step, true);
  });

  createEffect(() => {
    const key = `${props.sessionId}:${props.step}`;
    if (key === lastSwitchEvent) {
      return;
    }
    lastSwitchEvent = key;
    window.dispatchEvent(new CustomEvent("tifl:session-panel-switch", {
      detail: { session_id: props.sessionId, step: props.step },
    }));
  });

  createEffect(() => {
    const detail = state.detail;
    if (props.step !== "read" || detail?.content_type !== "phrase_set" || detail.status !== "ready") {
      return;
    }
    if (state.contentStatus === "ready") {
      void markReadingStarted();
    }
  });

  const title = createMemo(() => state.detail ? sessionTitle(state.detail) : "Session");
  const completed = createMemo(() => state.detail?.tasks.completed ?? state.tasks.filter((task) => task.graded).length);
  const total = createMemo(() => state.detail?.tasks.total ?? state.tasks.length);
  const canUseTasks = createMemo(() => {
    const status = state.detail?.status;
    return status === "reading" || status === "complete";
  });
  const canUseReview = createMemo(() => state.detail?.status === "complete");
  const activeLocked = createMemo(() => !isStepUnlocked(props.step, canUseTasks(), canUseReview()));

  async function loadSession(sessionID: string) {
    const seq = ++loadSeq;
    loadAbort?.abort();
    const controller = new AbortController();
    loadAbort = controller;
    setActionError("");
    batch(() => {
      setState("detail", null);
      setState("content", null);
      setState("story", null);
      setState("tasks", reconcile([]));
      setState("detailStatus", "loading");
      setState("contentStatus", "idle");
      setState("storyStatus", "idle");
      setState("tasksStatus", "loading");
      setState("errorMessage", "");
    });

    const finish = appStore.beginOperation();
    try {
      const detail = await getSessionDetail(sessionID, { signal: controller.signal });
      if (!isCurrentLoad(seq, controller)) {
        return;
      }
      batch(() => {
        setState("detail", detail);
        setState("detailStatus", "ready");
        setState("tasksStatus", shouldLoadSessionData(detail) ? "loading" : "ready");
        setState("contentStatus", shouldLoadSessionData(detail) ? "loading" : "idle");
        setState("storyStatus", detail.content_type === "story" && shouldLoadSessionData(detail) ? "loading" : "idle");
      });
      if (shouldLoadSessionData(detail)) {
        await Promise.all([
          loadContent(sessionID, seq, controller),
          loadTasks(sessionID, seq, controller),
        ]);
      }
    } catch (error) {
      if (isAbort(error)) {
        return;
      }
      if (isCurrentLoad(seq, controller)) {
        batch(() => {
          setState("detailStatus", "error");
          setState("tasksStatus", "error");
          setState("errorMessage", sessionLoadErrorMessage(error));
        });
      }
    } finally {
      finish();
    }
  }

  async function loadContent(sessionID: string, seq: number, controller: AbortController) {
    try {
      const content = await getSessionContent(sessionID, { signal: controller.signal });
      if (!isCurrentLoad(seq, controller)) {
        return;
      }
      batch(() => {
        setState("content", content);
        setState("contentStatus", "ready");
      });
      if (content.story?.story_id) {
        await loadStory(content.story.story_id, seq, controller);
      }
    } catch (error) {
      if (!isAbort(error) && isCurrentLoad(seq, controller)) {
        batch(() => {
          setState("contentStatus", "error");
          setState("storyStatus", "error");
        });
      }
    }
  }

  async function loadStory(storyID: string, seq: number, controller: AbortController) {
    try {
      const story = await getStory(storyID, { signal: controller.signal });
      if (!isCurrentLoad(seq, controller)) {
        return;
      }
      batch(() => {
        setState("story", story);
        setState("storyStatus", "ready");
      });
    } catch (error) {
      if (!isAbort(error) && isCurrentLoad(seq, controller)) {
        setState("storyStatus", "error");
      }
    }
  }

  async function loadTasks(sessionID: string, seq: number, controller: AbortController) {
    try {
      const data = await getSessionTasks(sessionID, { signal: controller.signal });
      if (!isCurrentLoad(seq, controller)) {
        return;
      }
      batch(() => {
        setState("tasks", reconcile(data.tasks));
        setState("tasksStatus", "ready");
        updateTaskProgress(data.total, data.completed);
      });
    } catch (error) {
      if (!isAbort(error) && isCurrentLoad(seq, controller)) {
        setState("tasksStatus", "error");
      }
    }
  }

  function refreshFromGeneration() {
    void loadSession(props.sessionId);
  }

  function noteReadingStarted() {
    if (!state.detail || state.detail.status !== "ready") {
      return;
    }
    batch(() => {
      setState("detail", "status", "reading");
      if (!state.detail?.reading_started_at) {
        setState("detail", "reading_started_at", unixNow());
      }
    });
  }

  async function markReadingStarted() {
    if (readingStartPending || !state.detail || state.detail.status !== "ready") {
      return;
    }
    const sessionID = state.detail.session_id;
    readingStartPending = true;
    try {
      await startReading(sessionID);
      if (isActiveSession(sessionID)) {
        noteReadingStarted();
      }
    } catch {
      if (isActiveSession(sessionID)) {
        appStore.showToast("This session could not be marked as started.", "error");
      }
    } finally {
      readingStartPending = false;
    }
  }

  async function submitTaskAt(index: number, request: SubmitTaskRequest) {
    const task = state.tasks[index];
    if (!task) {
      return;
    }
    const sessionID = state.detail?.session_id ?? props.sessionId;
    const taskID = task.task_id;
    const result = await submitTask(task.task_id, request);
    if (!isActiveTask(sessionID, index, taskID)) {
      return;
    }
    const wasGraded = Boolean(state.tasks[index]?.graded);
    const nextCompleted = wasGraded ? completed() : completed() + 1;
    batch(() => {
      setState("tasks", index, "grade", result.grade);
      setState("tasks", index, "graded", true);
      setState("tasks", index, "attempt_count", result.attempt_count);
      if (!wasGraded) {
        updateTaskProgress(total(), nextCompleted);
      }
    });
    announceGrade(result.grade, result.skill_xp);
  }

  async function completeCurrentSession() {
    if (completing() || state.detail?.status === "complete") {
      return;
    }
    const sessionID = state.detail?.session_id ?? props.sessionId;
    setActionError("");
    setCompleting(true);
    const finish = appStore.beginOperation();
    try {
      await completeSession(sessionID);
      if (!isActiveSession(sessionID)) {
        return;
      }
      noteCompleted();
      window.location.hash = sessionHref(sessionID, "review");
    } catch (error) {
      if (isActiveSession(sessionID)) {
        setActionError(completeErrorMessage(error));
      }
    } finally {
      finish();
      setCompleting(false);
    }
  }

  function noteCompleted() {
    if (!state.detail) {
      return;
    }
    batch(() => {
      setState("detail", "status", "complete");
      if (!state.detail?.completed_at) {
        setState("detail", "completed_at", unixNow());
      }
    });
  }

  function startNextSession() {
    const detail = state.detail;
    if (detail) {
      writeStartDraft({
        mode: detail.session_type,
        topic: detail.topic,
        expressions: detail.user_expressions,
        expressionOutput: detail.expression_output,
      });
    }
    window.location.hash = routeHref("/start");
  }

  function updateTaskProgress(nextTotal: number, nextCompleted: number) {
    if (!state.detail) {
      return;
    }
    const safeCompleted = Math.min(nextTotal, nextCompleted);
    setState("detail", "tasks", {
      total: nextTotal,
      completed: safeCompleted,
      pending: Math.max(0, nextTotal - safeCompleted),
    });
  }

  function resetVisited(step: SessionStep) {
    batch(() => {
      setVisited("read", step === "read");
      setVisited("tasks", step === "tasks");
      setVisited("review", step === "review");
    });
  }

  function isCurrentLoad(seq: number, controller: AbortController): boolean {
    return seq === loadSeq && !controller.signal.aborted;
  }

  function isActiveSession(sessionID: string): boolean {
    return props.sessionId === sessionID && activeSessionID === sessionID && state.detail?.session_id === sessionID;
  }

  function isActiveTask(sessionID: string, index: number, taskID: string): boolean {
    return isActiveSession(sessionID) && state.tasks[index]?.task_id === taskID;
  }

  const completeAction = () => (
    <button
      class="primary-button"
      type="button"
      disabled={completing() || state.detail?.status === "complete"}
      onClick={() => void completeCurrentSession()}
    >
      {completing() ? "Completing..." : state.detail?.status === "complete" ? "Completed" : "Complete session"}
    </button>
  );

  return (
    <section class="session-shell">
      <Switch>
        <Match when={state.detailStatus === "loading"}>
          <div class="session-shell-state" aria-busy="true">Loading session...</div>
        </Match>
        <Match when={state.detailStatus === "error"}>
          <div class="session-shell-state" role="alert">
            <p>{state.errorMessage || "This session could not be loaded."}</p>
            <button class="secondary-button" type="button" onClick={() => void loadSession(props.sessionId)}>Retry</button>
          </div>
        </Match>
        <Match when={state.detail}>
          {(detail) => (
            <>
              <header class="session-shell-header">
                <div class="session-shell-title">
                  <a class="session-back-link" href={routeHref("/")}>Home</a>
                  <h1>{title()}</h1>
                  <dl class="session-shell-meta" aria-label="Session metadata">
                    <div>
                      <dt>Status</dt>
                      <dd><span class="status-chip" data-status={detail().status}>{statusLabel(detail().status)}</span></dd>
                    </div>
                    <div>
                      <dt>Language</dt>
                      <dd>{detail().language.toUpperCase()}</dd>
                    </div>
                    <div>
                      <dt>Level</dt>
                      <dd>{formatLevel(detail().level)}</dd>
                    </div>
                    <Show when={detail().completed_at}>
                      {(completedAt) => (
                        <div>
                          <dt>Completed</dt>
                          <dd>{formatUnixSeconds(completedAt())}</dd>
                        </div>
                      )}
                    </Show>
                  </dl>
                </div>
                <SessionStepper
                  sessionId={props.sessionId}
                  active={props.step}
                  tasksUnlocked={canUseTasks()}
                  reviewUnlocked={canUseReview()}
                />
              </header>

              <Show when={actionError()}>
                <p class="form-error" role="alert">{actionError()}</p>
              </Show>

              <Show when={activeLocked()}>
                <LockedStep step={props.step} detail={detail()} />
              </Show>

              <div class="session-panels" hidden={activeLocked()}>
                <Show when={visited.read}>
                  <div class="session-panel" role="tabpanel" id="session-panel-read" aria-labelledby="session-tab-read" hidden={props.step !== "read"}>
                    <ReadPanel
                      detail={detail()}
                      content={state.content}
                      contentStatus={state.contentStatus}
                      story={state.story}
                      storyStatus={state.storyStatus}
                      active={props.step === "read"}
                      onReady={refreshFromGeneration}
                      onReadingStarted={noteReadingStarted}
                      onCompleted={() => {
                        noteCompleted();
                        window.location.hash = sessionHref(props.sessionId, "review");
                      }}
                    />
                  </div>
                </Show>

                <Show when={visited.tasks}>
                  <div class="session-panel" role="tabpanel" id="session-panel-tasks" aria-labelledby="session-tab-tasks" hidden={props.step !== "tasks"}>
                    <TasksPanel
                      status={state.tasksStatus}
                      tasks={state.tasks}
                      total={total()}
                      completed={completed()}
                      onRetry={() => void loadSession(props.sessionId)}
                      onSubmit={submitTaskAt}
                      completeAction={completeAction()}
                    />
                  </div>
                </Show>

                <Show when={visited.review}>
                  <div class="session-panel" role="tabpanel" id="session-panel-review" aria-labelledby="session-tab-review" hidden={props.step !== "review"}>
                    <ReviewPanel
                      detail={detail()}
                      content={state.content}
                      contentStatus={state.contentStatus}
                      story={state.story}
                      storyStatus={state.storyStatus}
                      tasks={state.tasks}
                      tasksStatus={state.tasksStatus}
                      onRead={() => {
                        window.location.hash = sessionHref(props.sessionId, "read");
                      }}
                      onStartNext={startNextSession}
                    />
                  </div>
                </Show>
              </div>
            </>
          )}
        </Match>
      </Switch>
    </section>
  );
}

function SessionStepper(props: {
  sessionId: string;
  active: SessionStep;
  tasksUnlocked: boolean;
  reviewUnlocked: boolean;
}) {
  const unlocked = (step: SessionStep) => isStepUnlocked(step, props.tasksUnlocked, props.reviewUnlocked);
  return (
    <nav class="session-stepper" role="tablist" aria-label="Session steps">
      <For each={STEPS}>
        {(step, index) => (
          <Show
            when={unlocked(step.id)}
            fallback={
              <span
                id={`session-tab-${step.id}`}
                class="session-step"
                role="tab"
                aria-selected={props.active === step.id}
                aria-disabled="true"
                aria-controls={`session-panel-${step.id}`}
                data-active={props.active === step.id ? "" : undefined}
                data-locked=""
              >
                <span>{index() + 1}</span>
                {step.label}
              </span>
            }
          >
            <a
              id={`session-tab-${step.id}`}
              class="session-step"
              role="tab"
              aria-selected={props.active === step.id}
              aria-controls={`session-panel-${step.id}`}
              data-active={props.active === step.id ? "" : undefined}
              href={sessionHref(props.sessionId, step.id)}
            >
              <span>{index() + 1}</span>
              {step.label}
            </a>
          </Show>
        )}
      </For>
    </nav>
  );
}

function ReadPanel(props: {
  detail: SessionDetail;
  content: SessionContent | null;
  contentStatus: LoadStatus;
  story: StoryLoad | null;
  storyStatus: LoadStatus;
  active: boolean;
  onReady: () => void;
  onReadingStarted: () => void;
  onCompleted: () => void;
}) {
  if (props.detail.status === "pending" || props.detail.status === "generating" || props.detail.status === "failed") {
    return <GenerationView sessionId={props.detail.session_id} onReady={props.onReady} />;
  }
  if (props.detail.content_type === "phrase_set") {
    return (
      <PhrasesPanel
        status={props.contentStatus === "error" ? "error" : props.contentStatus === "ready" ? "ready" : "loading"}
        content={props.content}
        sessionId={props.detail.session_id}
      />
    );
  }
  return (
    <Switch>
      <Match when={props.storyStatus === "loading" || props.contentStatus === "loading"}>
        <div class="tasks-state" aria-busy="true">Loading story...</div>
      </Match>
      <Match when={props.storyStatus === "error" || props.contentStatus === "error"}>
        <div class="tasks-state" role="alert">This story could not be loaded.</div>
      </Match>
      <Match when={props.story && (props.content?.story?.story_id || props.detail.story_id)}>
        {(storyID) => (
          <ReaderView
            storyId={storyID()}
            sessionId={props.detail.session_id}
            story={props.story}
            active={props.active}
            onReadingStarted={props.onReadingStarted}
            onSessionComplete={props.onCompleted}
          />
        )}
      </Match>
      <Match when={!props.detail.story_id}>
        <div class="tasks-state empty-state">
          <h2>No story yet</h2>
          <p>This session does not have story content attached.</p>
        </div>
      </Match>
    </Switch>
  );
}

function ReviewPanel(props: {
  detail: SessionDetail;
  content: SessionContent | null;
  contentStatus: LoadStatus;
  story: StoryLoad | null;
  storyStatus: LoadStatus;
  tasks: readonly Task[];
  tasksStatus: TaskLoadStatus;
  onRead: () => void;
  onStartNext: () => void;
}) {
  const storyText = createMemo(() => props.story?.tokens.map((token) => token.surface).join("") ?? "");
  const selectedTotal = createMemo(() => props.detail.selected_counts.targets + props.detail.selected_counts.new);

  return (
    <section class="review-panel">
      <header class="review-outcome">
        <div>
          <p class="review-kicker">Session complete</p>
          <h2>{sessionTitle(props.detail)}</h2>
          <p>{props.detail.completed_at ? formatUnixSeconds(props.detail.completed_at) : "Completed just now"}</p>
        </div>
      </header>

      <ReviewSection title="What you worked on">
        <ContentRecap
          detail={props.detail}
          content={props.content}
          contentStatus={props.contentStatus}
          storyText={storyText()}
          storyStatus={props.storyStatus}
          onRead={props.onRead}
        />
      </ReviewSection>

      <ReviewSection title="Task results">
        <TaskResults tasks={props.tasks} status={props.tasksStatus} />
      </ReviewSection>

      <Show when={selectedTotal() > 0}>
        <ReviewSection title="What it earned">
          <dl class="review-receipt">
            <Show when={props.detail.selected_counts.targets > 0}>
              <div>
                <dt>Targets practiced</dt>
                <dd>{props.detail.selected_counts.targets}</dd>
              </div>
            </Show>
            <Show when={props.detail.selected_counts.new > 0}>
              <div>
                <dt>New items introduced</dt>
                <dd>{props.detail.selected_counts.new}</dd>
              </div>
            </Show>
          </dl>
        </ReviewSection>
      </Show>

      <div class="review-actions">
        <button class="primary-button" type="button" onClick={props.onStartNext}>Start next session</button>
        <button class="secondary-button" type="button" onClick={props.onRead}>Re-read</button>
        <a class="button-link secondary-link" href={routeHref("/")}>Home</a>
      </div>
    </section>
  );
}

function ReviewSection(props: { title: string; children: JSX.Element }) {
  return (
    <section class="review-section">
      <h3>{props.title}</h3>
      {props.children}
    </section>
  );
}

function ContentRecap(props: {
  detail: SessionDetail;
  content: SessionContent | null;
  contentStatus: LoadStatus;
  storyText: string;
  storyStatus: LoadStatus;
  onRead: () => void;
}) {
  if (props.detail.content_type === "phrase_set") {
    const items = createMemo(() => props.content?.phrase_set?.items ?? []);
    return (
      <Switch>
        <Match when={props.contentStatus === "loading"}>
          <div class="tasks-state" aria-busy="true">Loading phrases...</div>
        </Match>
        <Match when={props.contentStatus === "error"}>
          <div class="tasks-state" role="alert">Phrase content could not be loaded.</div>
        </Match>
        <Match when={items().length === 0}>
          <p class="review-muted">No phrase content is attached to this session.</p>
        </Match>
        <Match when={items().length > 0}>
          <ol class="review-phrase-recap">
            <For each={items()}>
              {(item) => (
                <li>
                  <strong lang={props.content?.phrase_set?.language || undefined}>{item.target_text}</strong>
                  <Show when={item.gloss}><span>{item.gloss}</span></Show>
                </li>
              )}
            </For>
          </ol>
        </Match>
      </Switch>
    );
  }
  return (
    <Switch>
      <Match when={props.storyStatus === "loading" || props.contentStatus === "loading"}>
        <div class="tasks-state" aria-busy="true">Loading story...</div>
      </Match>
      <Match when={props.storyStatus === "error" || props.contentStatus === "error"}>
        <div class="tasks-state" role="alert">Story content could not be loaded.</div>
      </Match>
      <Match when={props.storyText}>
        <div class="review-story-recap">
          <p lang={props.detail.language}>{props.storyText}</p>
          <button class="secondary-button" type="button" onClick={props.onRead}>Read again</button>
        </div>
      </Match>
      <Match when={!props.storyText}>
        <p class="review-muted">No story content is attached to this session.</p>
      </Match>
    </Switch>
  );
}

function TaskResults(props: { tasks: readonly Task[]; status: TaskLoadStatus }) {
  return (
    <Switch>
      <Match when={props.status === "loading"}>
        <div class="tasks-state" aria-busy="true">Loading task results...</div>
      </Match>
      <Match when={props.status === "error"}>
        <div class="tasks-state" role="alert">Task results could not be loaded.</div>
      </Match>
      <Match when={props.tasks.length === 0}>
        <div class="tasks-state empty-state">
          <h2>No tasks in this session</h2>
          <p>This was a content-only session.</p>
        </div>
      </Match>
      <Match when={props.tasks.length > 0}>
        <ol class="review-task-list">
          <For each={props.tasks}>
            {(task, index) => <ReviewTask task={task} position={index() + 1} />}
          </For>
        </ol>
      </Match>
    </Switch>
  );
}

function ReviewTask(props: { task: Task; position: number }) {
  const grade = () => props.task.grade;
  return (
    <li>
      <article class="review-task" data-state={props.task.graded ? grade()?.correct ? "correct" : "incorrect" : "skipped"}>
        <header>
          <span>Task {props.position}</span>
          <strong>{taskTypeLabel(props.task.task_type)}</strong>
        </header>
        <p class="review-task-prompt">{taskPrompt(props.task)}</p>
        <Show
          when={props.task.graded && grade()}
          fallback={<p class="review-task-status" data-state="skipped">Not answered</p>}
        >
          {(currentGrade) => (
            <div class="review-grade">
              <p class="review-task-status" data-state={currentGrade().correct ? "correct" : "incorrect"}>
                {currentGrade().correct ? "Correct" : "Not quite"} · {Math.round(currentGrade().score * 100)}%
              </p>
              <Show when={currentGrade().feedback}>
                {(feedback) => <p>{feedback()}</p>}
              </Show>
              <Show when={(currentGrade().items_demonstrated?.length ?? 0) > 0}>
                <p class="review-muted">
                  Demonstrated {currentGrade().items_demonstrated!.length} knowledge item
                  {currentGrade().items_demonstrated!.length === 1 ? "" : "s"}.
                </p>
              </Show>
            </div>
          )}
        </Show>
      </article>
    </li>
  );
}

function LockedStep(props: { step: SessionStep; detail: SessionDetail }) {
  const message = props.step === "review"
    ? "Review unlocks once the session is complete."
    : "Tasks unlock once reading has started.";
  return (
    <div class="session-shell-state" role="status">
      <p>{message}</p>
      <a class="button-link" href={sessionHref(props.detail.session_id, "read")}>Go to Read</a>
    </div>
  );
}

function isStepUnlocked(step: SessionStep, tasksUnlocked: boolean, reviewUnlocked: boolean): boolean {
  if (step === "read") {
    return true;
  }
  if (step === "tasks") {
    return tasksUnlocked;
  }
  return reviewUnlocked;
}

function shouldLoadSessionData(detail: SessionDetail): boolean {
  return detail.status === "ready" || detail.status === "reading" || detail.status === "complete";
}

function isAbort(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

function unixNow(): number {
  return Math.floor(Date.now() / 1000);
}

function statusLabel(status: SessionDetail["status"]): string {
  switch (status) {
    case "generating":
      return "Generating";
    case "ready":
      return "Ready";
    case "reading":
      return "Reading";
    case "complete":
      return "Complete";
    case "failed":
      return "Failed";
    default:
      return "Pending";
  }
}

function sessionLoadErrorMessage(error: unknown): string {
  if (error instanceof APIError && error.status === 404) {
    return "This session is no longer available.";
  }
  return "This session could not be loaded.";
}

function completeErrorMessage(error: unknown): string {
  if (error instanceof APIError && error.status === 404) {
    return "This session is no longer available.";
  }
  return "This session could not be completed.";
}

function taskTypeLabel(taskType: string): string {
  switch (taskType) {
    case "comprehension_mc":
      return "Comprehension";
    case "fill_blank":
      return "Fill in the blank";
    case "production":
      return "Production";
    default:
      return taskType;
  }
}

function taskPrompt(task: Task): string {
  const content = task.content;
  const question = readString(content, "question");
  if (question) {
    return question;
  }
  const sentence = readString(content, "sentence");
  if (sentence) {
    return sentence;
  }
  const prompt = readString(content, "prompt_l1");
  if (prompt) {
    return prompt;
  }
  return "Task prompt unavailable.";
}

function readString(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  return typeof value === "string" ? value : "";
}
