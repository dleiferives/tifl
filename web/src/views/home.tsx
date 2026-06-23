import { createMemo, createSignal, For, Match, onMount, Show, Switch } from "solid-js";
import {
  APIError,
  generateSession,
  listSessions,
  retrySession,
  type APIRequest,
  type APISchema,
} from "../api";
import { routeHref } from "../router";
import { appStore } from "../store";

type SessionOverview = APISchema<"SessionOverview">;
type SessionStatus = SessionOverview["status"];
type GenerateRequest = APIRequest<"generateSession">;

interface SessionGroup {
  id: string;
  title: string;
  description: string;
  statuses: SessionStatus[];
}

const PAGE_SIZE = 20;
const SESSION_GROUPS: SessionGroup[] = [
  {
    id: "active",
    title: "Generating",
    description: "Sessions waiting on the generation pipeline.",
    statuses: ["generating", "pending"],
  },
  {
    id: "resume",
    title: "Resume",
    description: "Ready or in-progress sessions.",
    statuses: ["ready", "reading"],
  },
  {
    id: "failed",
    title: "Failed",
    description: "Sessions that need a retry or generation details.",
    statuses: ["failed"],
  },
  {
    id: "complete",
    title: "Complete",
    description: "Finished sessions available for review.",
    statuses: ["complete"],
  },
];

const dateFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
});

export function HomeView() {
  const [sessions, setSessions] = createSignal<SessionOverview[]>([]);
  const [loading, setLoading] = createSignal(true);
  const [loadingMore, setLoadingMore] = createSignal(false);
  const [hasMore, setHasMore] = createSignal(false);
  const [listError, setListError] = createSignal("");
  const [actionError, setActionError] = createSignal("");
  const [starting, setStarting] = createSignal(false);
  const [retryingSessionID, setRetryingSessionID] = createSignal("");

  const totalTasks = createMemo(() => sessions().reduce((sum, session) => sum + session.tasks.total, 0));
  const completedTasks = createMemo(() => sessions().reduce((sum, session) => sum + session.tasks.completed, 0));
  const resumeCount = createMemo(() => sessions().filter((session) => session.status === "ready" || session.status === "reading").length);
  const failedCount = createMemo(() => sessions().filter((session) => session.status === "failed").length);

  onMount(() => {
    void loadSessions(true);
  });

  const loadSessions = async (reset: boolean) => {
    setListError("");
    if (reset) {
      setLoading(true);
    } else {
      setLoadingMore(true);
    }
    const finish = appStore.beginOperation();
    try {
      const offset = reset ? 0 : sessions().length;
      const page = await listSessions({ limit: PAGE_SIZE, offset });
      setSessions(reset ? page.sessions : [...sessions(), ...page.sessions]);
      setHasMore(page.has_more);
    } catch (error) {
      setListError(sessionListErrorMessage(error));
    } finally {
      finish();
      setLoading(false);
      setLoadingMore(false);
    }
  };

  const startSystemSession = async () => {
    setActionError("");
    setStarting(true);
    const finish = appStore.beginOperation();
    try {
      const request: GenerateRequest = { session_type: "system" };
      if (appStore.activeLanguage()) {
        request.language = appStore.activeLanguage();
      }
      if (appStore.currentLevel()) {
        request.level = appStore.currentLevel();
      }
      const next = await generateSession(request);
      window.location.hash = routeHref(`/generation/${encodeURIComponent(next.session_id)}`);
    } catch (error) {
      setActionError(startSessionErrorMessage(error));
    } finally {
      finish();
      setStarting(false);
    }
  };

  const retryFailedSession = async (sessionID: string) => {
    setActionError("");
    setRetryingSessionID(sessionID);
    const finish = appStore.beginOperation();
    try {
      const next = await retrySession(sessionID);
      window.location.hash = routeHref(`/generation/${encodeURIComponent(next.session_id)}`);
    } catch (error) {
      setActionError(retrySessionErrorMessage(error));
    } finally {
      finish();
      setRetryingSessionID("");
    }
  };

  const sessionsFor = (group: SessionGroup) => sessions().filter((session) => group.statuses.includes(session.status));

  return (
    <section class="home-view">
      <header class="view-heading home-heading">
        <div>
          <h1>Home</h1>
          <p>{homeSubtitle()}</p>
        </div>
        <div class="home-start-actions">
          <button class="primary-button" type="button" disabled={starting()} onClick={startSystemSession}>
            {starting() ? "Starting..." : "Start session"}
          </button>
          <a class="button-link secondary-link" href={routeHref("/start")}>Guided session…</a>
        </div>
      </header>

      <Show when={actionError()}>
        <p class="form-error" role="alert">{actionError()}</p>
      </Show>

      <div class="home-metrics" aria-label="Session summary">
        <Metric label="Resume" value={resumeCount()} />
        <Metric label="Failed" value={failedCount()} />
        <Metric label="Tasks" value={`${completedTasks()}/${totalTasks()}`} />
      </div>

      <Switch>
        <Match when={loading()}>
          <div class="home-state" aria-busy="true">Loading sessions...</div>
        </Match>
        <Match when={listError()}>
          <div class="home-state" role="alert">
            <p>{listError()}</p>
            <button class="secondary-button" type="button" onClick={() => void loadSessions(true)}>
              Retry
            </button>
          </div>
        </Match>
        <Match when={sessions().length === 0}>
          <div class="home-state empty-state">
            <h2>No sessions yet</h2>
            <p>Start a session to generate your first story and tasks.</p>
          </div>
        </Match>
        <Match when={sessions().length > 0}>
          <div class="session-groups">
            <For each={SESSION_GROUPS}>
              {(group) => {
                const groupedSessions = () => sessionsFor(group);
                return (
                  <Show when={groupedSessions().length > 0}>
                    <section class="session-group" aria-labelledby={`session-group-${group.id}`}>
                      <header class="session-group-heading">
                        <div>
                          <h2 id={`session-group-${group.id}`}>{group.title}</h2>
                          <p>{group.description}</p>
                        </div>
                        <span>{groupedSessions().length}</span>
                      </header>
                      <div class="session-list">
                        <For each={groupedSessions()}>
                          {(session) => (
                            <SessionRow
                              session={session}
                              retrying={retryingSessionID() === session.session_id}
                              onRetry={retryFailedSession}
                            />
                          )}
                        </For>
                      </div>
                    </section>
                  </Show>
                );
              }}
            </For>
          </div>
          <Show when={hasMore()}>
            <div class="load-more-row">
              <button class="secondary-button" type="button" disabled={loadingMore()} onClick={() => void loadSessions(false)}>
                {loadingMore() ? "Loading..." : "Load more"}
              </button>
            </div>
          </Show>
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

function SessionRow(props: {
  session: SessionOverview;
  retrying: boolean;
  onRetry: (sessionID: string) => Promise<void>;
}) {
  return (
    <article class="session-row">
      <div class="session-row-main">
        <div class="session-title-line">
          <h3>{sessionTitle(props.session)}</h3>
          <span class="status-chip" data-status={props.session.status}>{statusLabel(props.session.status)}</span>
        </div>
        <dl class="session-meta" aria-label="Session metadata">
          <div>
            <dt>Language</dt>
            <dd>{props.session.language.toUpperCase()}</dd>
          </div>
          <div>
            <dt>Level</dt>
            <dd>{formatLevel(props.session.level)}</dd>
          </div>
          <div>
            <dt>Created</dt>
            <dd>{formatUnixSeconds(props.session.created_at)}</dd>
          </div>
          <div>
            <dt>Tasks</dt>
            <dd>{props.session.tasks.completed}/{props.session.tasks.total}</dd>
          </div>
        </dl>
        <p class="session-detail-line">
          {sessionDetailLine(props.session)}
        </p>
      </div>
      <div class="session-actions">
        <SessionActions session={props.session} retrying={props.retrying} onRetry={props.onRetry} />
      </div>
    </article>
  );
}

function SessionActions(props: {
  session: SessionOverview;
  retrying: boolean;
  onRetry: (sessionID: string) => Promise<void>;
}) {
  const session = props.session;
  return (
    <>
      <Show when={session.status === "pending" || session.status === "generating"}>
        <a class="button-link secondary-link" href={generationHref(session.session_id)}>Generation</a>
      </Show>
      <Show when={session.story_id && (session.status === "ready" || session.status === "reading" || session.status === "complete")}>
        <a class="button-link" href={readerHref(session.story_id || "")}>Reader</a>
      </Show>
      <Show when={session.tasks.total > 0 && (session.status === "ready" || session.status === "reading" || session.status === "complete")}>
        <a class="button-link secondary-link" href={tasksHref(session.session_id)}>Tasks</a>
      </Show>
      <Show when={session.status === "failed"}>
        <button class="primary-button" type="button" disabled={props.retrying} onClick={() => void props.onRetry(session.session_id)}>
          {props.retrying ? "Retrying..." : "Retry"}
        </button>
        <a class="button-link secondary-link" href={generationHref(session.session_id)}>Details</a>
      </Show>
      <Show when={session.status === "ready" && !session.story_id}>
        <a class="button-link secondary-link" href={generationHref(session.session_id)}>Generation</a>
      </Show>
    </>
  );
}

function homeSubtitle(): string {
  const language = appStore.activeLanguage();
  const level = appStore.currentLevel();
  if (language && level) {
    return `${language.toUpperCase()} · ${formatLevel(level)} default`;
  }
  return "Sessions and study controls.";
}

function sessionTitle(session: SessionOverview): string {
  if (session.topic) {
    return session.topic;
  }
  if (session.session_type === "expression_guided" && session.user_expressions?.length) {
    return session.user_expressions.slice(0, 2).join(", ");
  }
  return `${formatSessionType(session.session_type)} session`;
}

function sessionDetailLine(session: SessionOverview): string {
  const selected = `${session.selected_counts.targets} targets, ${session.selected_counts.new} new`;
  if (session.status === "failed") {
    return `${selected}. Retry generation or open the generation route for failure details.`;
  }
  if (session.status === "generating" || session.status === "pending") {
    return `${selected}. Generation is in progress.`;
  }
  if (session.tasks.total === 0) {
    return `${selected}. No tasks are attached yet.`;
  }
  return `${selected}. ${session.tasks.pending} tasks pending.`;
}

function formatSessionType(sessionType: SessionOverview["session_type"]): string {
  switch (sessionType) {
    case "topic_guided":
      return "Topic-guided";
    case "expression_guided":
      return "Expression-guided";
    default:
      return "System";
  }
}

function statusLabel(status: SessionStatus): string {
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

function formatLevel(level: string): string {
  return level.split("-").map((part) => part.charAt(0).toUpperCase() + part.slice(1)).join(" ");
}

function formatUnixSeconds(value: number): string {
  if (!Number.isFinite(value)) {
    return "Unknown";
  }
  return dateFormatter.format(new Date(value * 1000));
}

function generationHref(sessionID: string): string {
  return routeHref(`/generation/${encodeURIComponent(sessionID)}`);
}

function readerHref(storyID: string): string {
  return routeHref(`/reader/${encodeURIComponent(storyID)}`);
}

function tasksHref(sessionID: string): string {
  return routeHref(`/tasks/${encodeURIComponent(sessionID)}`);
}

function sessionListErrorMessage(error: unknown): string {
  if (error instanceof APIError && error.status === 401) {
    return "Sign in again to load your sessions.";
  }
  return "Sessions could not be loaded.";
}

function startSessionErrorMessage(error: unknown): string {
  if (error instanceof APIError) {
    if (error.status === 503) {
      return "Generation is not configured. Use existing demo sessions or start the gateway.";
    }
    if (error.status === 400) {
      return "Your current language or level cannot start a session.";
    }
  }
  return "A new session could not be started.";
}

function retrySessionErrorMessage(error: unknown): string {
  if (error instanceof APIError && error.status === 503) {
    return "Generation is not configured. Start the gateway before retrying.";
  }
  return "The session could not be retried.";
}
