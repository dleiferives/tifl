import { createMemo, createSignal, For, Match, onMount, Show, Switch } from "solid-js";
import {
  APIError,
  archiveSession,
  deleteSession,
  listSessions,
  retrySession,
  unarchiveSession,
} from "../api";
import { ConfirmDialog, type ConfirmDialogRequest } from "../components/confirm_dialog";
import { routeHref, sessionHref } from "../router";
import { appStore } from "../store";
import { formatLevel, SessionRow, sessionTitle, type SessionOverview, type SessionStatus } from "./session_rows";

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
    title: "Preparing",
    description: "Stories and tasks that are still being prepared.",
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

export function HomeView() {
  const [sessions, setSessions] = createSignal<SessionOverview[]>([]);
  const [loading, setLoading] = createSignal(true);
  const [loadingMore, setLoadingMore] = createSignal(false);
  const [hasMore, setHasMore] = createSignal(false);
  const [listError, setListError] = createSignal("");
  const [actionError, setActionError] = createSignal("");
  const [retryingSessionID, setRetryingSessionID] = createSignal("");
  const [archivedView, setArchivedView] = createSignal(false);
  const [busyAction, setBusyAction] = createSignal("");
  const [confirmRequest, setConfirmRequest] = createSignal<ConfirmDialogRequest | null>(null);

  const totalTasks = createMemo(() => sessions().reduce((sum, session) => sum + session.tasks.total, 0));
  const completedTasks = createMemo(() => sessions().reduce((sum, session) => sum + session.tasks.completed, 0));
  const resumeCount = createMemo(() => sessions().filter((session) => session.status === "ready" || session.status === "reading").length);
  const failedCount = createMemo(() => sessions().filter((session) => session.status === "failed").length);

  onMount(() => {
    void loadSessions(true);
  });

  const loadSessions = async (reset: boolean, archived = archivedView()) => {
    setListError("");
    if (reset) {
      setLoading(true);
    } else {
      setLoadingMore(true);
    }
    const finish = appStore.beginOperation();
    try {
      const offset = reset ? 0 : sessions().length;
      const page = await listSessions({ limit: PAGE_SIZE, offset, archived });
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

  const showArchived = (archived: boolean) => {
    if (archivedView() === archived) {
      return;
    }
    setArchivedView(archived);
    setSessions([]);
    setHasMore(false);
    setActionError("");
    void loadSessions(true, archived);
  };

  const retryFailedSession = async (sessionID: string) => {
    setActionError("");
    setRetryingSessionID(sessionID);
    const finish = appStore.beginOperation();
    try {
      const next = await retrySession(sessionID);
      window.location.hash = sessionHref(next.session_id, "read");
    } catch (error) {
      setActionError(retrySessionErrorMessage(error));
    } finally {
      finish();
      setRetryingSessionID("");
    }
  };

  const archiveCurrentSession = async (sessionID: string) => {
    await runSessionAction("archive", sessionID, async () => archiveSession(sessionID));
  };

  const restoreSession = async (sessionID: string) => {
    await runSessionAction("restore", sessionID, async () => unarchiveSession(sessionID));
  };

  const deleteCurrentSession = async (session: SessionOverview) => {
    const title = sessionTitle(session);
    setConfirmRequest({
      title: `Delete "${title}"?`,
      message: "This deletes the session: its story, tasks, and your grades on them.",
      confirmLabel: "Delete session",
      onConfirm: () => runSessionAction("delete", session.session_id, async () => deleteSession(session.session_id)),
    });
  };

  const runSessionAction = async (kind: string, sessionID: string, action: () => Promise<void>) => {
    setActionError("");
    setBusyAction(`${kind}:${sessionID}`);
    const finish = appStore.beginOperation();
    try {
      await action();
      setSessions(sessions().filter((session) => session.session_id !== sessionID));
    } catch (error) {
      setActionError(sessionActionErrorMessage(kind, error));
    } finally {
      finish();
      setBusyAction("");
    }
  };

  const sessionsFor = (group: SessionGroup) => sessions().filter((session) => group.statuses.includes(session.status));

  return (
    <section class="home-view">
      <header class="view-heading home-heading">
        <div>
          <h1>Your stories</h1>
          <p>{homeSubtitle()}</p>
        </div>
        <div class="home-start-actions">
          <a class="button-link" href={routeHref("/import")}>Add story</a>
          <a class="button-link secondary-link" href={routeHref("/start")}>Generate a story</a>
          <a class="button-link secondary-link" href={routeHref("/library")}>Library</a>
        </div>
      </header>

      <Show when={actionError()}>
        <p class="form-error" role="alert">{actionError()}</p>
      </Show>

      <div class="home-metrics" aria-label="Story and task summary">
        <Metric label="Resume" value={resumeCount()} />
        <Metric label="Failed" value={failedCount()} />
        <Metric label="Tasks" value={`${completedTasks()}/${totalTasks()}`} />
      </div>

      <div class="home-list-toolbar" aria-label="Session list filter">
        <div class="segmented-control">
          <button type="button" aria-pressed={!archivedView()} onClick={() => showArchived(false)}>
            Active
          </button>
          <button type="button" aria-pressed={archivedView()} onClick={() => showArchived(true)}>
            Archived
          </button>
        </div>
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
            <h2>{archivedView() ? "No archived stories" : "No stories yet"}</h2>
            <p>{archivedView() ? "Archived stories will appear here." : "Add a story you want to study, or generate one with Tifl."}</p>
            <Show when={!archivedView()}>
              <div class="home-start-actions">
                <a class="button-link" href={routeHref("/import")}>Add your first story</a>
                <a class="button-link secondary-link" href={routeHref("/start")}>Generate one instead</a>
              </div>
            </Show>
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
                              archivedView={archivedView()}
                              busyAction={busyAction()}
                              onRetry={retryFailedSession}
                              onArchive={archiveCurrentSession}
                              onRestore={restoreSession}
                              onDelete={deleteCurrentSession}
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
      <ConfirmDialog request={confirmRequest()} onCancel={() => setConfirmRequest(null)} />
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

function homeSubtitle(): string {
  const language = appStore.activeLanguage();
  const level = appStore.currentLevel();
  if (language && level) {
    return `${language.toUpperCase()} · ${formatLevel(level)} reading`;
  }
  return "Add your own text and turn it into reading practice.";
}

function sessionListErrorMessage(error: unknown): string {
  if (error instanceof APIError && error.status === 401) {
    return "Sign in again to load your sessions.";
  }
  return "Sessions could not be loaded.";
}

function retrySessionErrorMessage(error: unknown): string {
  if (error instanceof APIError && error.status === 503) {
    return "Generation is not configured. Start the gateway before retrying.";
  }
  return "The session could not be retried.";
}

function sessionActionErrorMessage(kind: string, error: unknown): string {
  if (error instanceof APIError && error.status === 404) {
    return "That session is no longer available.";
  }
  if (kind === "archive") {
    return "The session could not be archived.";
  }
  if (kind === "restore") {
    return "The session could not be restored.";
  }
  return "The session could not be deleted.";
}
