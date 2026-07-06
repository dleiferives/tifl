import { createEffect, createMemo, createSignal, For, Match, Show, Switch, type JSX } from "solid-js";
import {
  APIError,
  archiveSession,
  deleteSession,
  deleteStory,
  listImportedStories,
  listSessions,
  retrySession,
  unarchiveSession,
  type APISchema,
} from "../api";
import { ConfirmDialog, type ConfirmDialogRequest } from "../components/confirm_dialog";
import { routeHref, sessionHref } from "../router";
import { appStore } from "../store";
import {
  formatLevel,
  formatUnixSeconds,
  LibrarySessionRow,
  readerHref,
  RowMenu,
  sessionTitle,
  type SessionOverview,
} from "./session_rows";

type ImportedStory = APISchema<"ImportedStory">;

const PAGE_SIZE = 20;

export function LibraryView() {
  const [allLanguages, setAllLanguages] = createSignal(false);
  const [generatedArchived, setGeneratedArchived] = createSignal(false);
  const [imports, setImports] = createSignal<ImportedStory[]>([]);
  const [sessions, setSessions] = createSignal<SessionOverview[]>([]);
  const [importsLoading, setImportsLoading] = createSignal(true);
  const [sessionsLoading, setSessionsLoading] = createSignal(true);
  const [importsLoadingMore, setImportsLoadingMore] = createSignal(false);
  const [sessionsLoadingMore, setSessionsLoadingMore] = createSignal(false);
  const [importsHasMore, setImportsHasMore] = createSignal(false);
  const [sessionsHasMore, setSessionsHasMore] = createSignal(false);
  const [importsError, setImportsError] = createSignal("");
  const [sessionsError, setSessionsError] = createSignal("");
  const [actionError, setActionError] = createSignal("");
  const [busyAction, setBusyAction] = createSignal("");
  const [retryingSessionID, setRetryingSessionID] = createSignal("");
  const [confirmRequest, setConfirmRequest] = createSignal<ConfirmDialogRequest | null>(null);

  const language = createMemo(() => allLanguages() ? "" : appStore.activeLanguage());
  const languageLabel = createMemo(() => language() ? language().toUpperCase() : "All languages");

  const loadImports = async (reset: boolean, currentLanguage = language()) => {
    setImportsError("");
    if (reset) {
      setImportsLoading(true);
    } else {
      setImportsLoadingMore(true);
    }
    const finish = appStore.beginOperation();
    try {
      const offset = reset ? 0 : imports().length;
      const page = await listImportedStories({ limit: PAGE_SIZE, offset, language: currentLanguage || undefined });
      setImports(reset ? page.stories : [...imports(), ...page.stories]);
      setImportsHasMore(page.has_more);
    } catch (error) {
      setImportsError(libraryListErrorMessage("imports", error));
    } finally {
      finish();
      setImportsLoading(false);
      setImportsLoadingMore(false);
    }
  };

  const loadSessionsPage = async (reset: boolean, currentLanguage = language(), archived = generatedArchived()) => {
    setSessionsError("");
    if (reset) {
      setSessionsLoading(true);
    } else {
      setSessionsLoadingMore(true);
    }
    const finish = appStore.beginOperation();
    try {
      const offset = reset ? 0 : sessions().length;
      const page = await listSessions({ limit: PAGE_SIZE, offset, archived, language: currentLanguage || undefined });
      setSessions(reset ? page.sessions : [...sessions(), ...page.sessions]);
      setSessionsHasMore(page.has_more);
    } catch (error) {
      setSessionsError(libraryListErrorMessage("sessions", error));
    } finally {
      finish();
      setSessionsLoading(false);
      setSessionsLoadingMore(false);
    }
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

  const deleteImported = async (story: ImportedStory) => {
    setConfirmRequest({
      title: `Delete "${story.title}"?`,
      message: "This deletes the imported text.",
      confirmLabel: "Delete text",
      onConfirm: () => runImportedAction("delete", story.story_id, async () => deleteStory(story.story_id)),
    });
  };

  const deleteGenerated = async (session: SessionOverview) => {
    const title = sessionTitle(session);
    setConfirmRequest({
      title: `Delete "${title}"?`,
      message: "This deletes the session: its story, tasks, and your grades on them.",
      confirmLabel: "Delete session",
      onConfirm: () => runSessionAction("delete", session.session_id, async () => {
        if (session.story_id) {
          await deleteStory(session.story_id);
          return;
        }
        await deleteSession(session.session_id);
      }),
    });
  };

  const runImportedAction = async (kind: string, storyID: string, action: () => Promise<void>) => {
    setActionError("");
    setBusyAction(`${kind}:import:${storyID}`);
    const finish = appStore.beginOperation();
    try {
      await action();
      setImports(imports().filter((story) => story.story_id !== storyID));
    } catch (error) {
      setActionError(importedActionErrorMessage(error));
    } finally {
      finish();
      setBusyAction("");
    }
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

  createEffect(() => {
    const currentLanguage = language();
    void loadImports(true, currentLanguage);
  });

  createEffect(() => {
    const currentLanguage = language();
    const archived = generatedArchived();
    void loadSessionsPage(true, currentLanguage, archived);
  });

  return (
    <section class="library-view">
      <header class="view-heading library-heading">
        <div>
          <h1>Library</h1>
          <p>{languageLabel()}</p>
        </div>
        <div class="library-heading-actions">
          <label class="toggle-control">
            <input type="checkbox" checked={allLanguages()} onChange={(event) => setAllLanguages(event.currentTarget.checked)} />
            <span>All languages</span>
          </label>
          <a class="button-link secondary-link" href={routeHref("/import")}>Import text</a>
          <a class="button-link secondary-link" href={routeHref("/start")}>Start session</a>
        </div>
      </header>

      <Show when={actionError()}>
        <p class="form-error" role="alert">{actionError()}</p>
      </Show>

      <div class="library-sections">
        <section class="library-section" aria-labelledby="library-imports">
          <header class="library-section-heading">
            <div>
              <h2 id="library-imports">Imported texts</h2>
              <p>{imports().length} shown</p>
            </div>
          </header>
          <LibrarySectionState
            loading={importsLoading()}
            error={importsError()}
            empty={imports().length === 0}
            loadingText="Loading imported texts..."
            emptyTitle="No imported texts"
            emptyText="Texts you import will appear here."
            emptyHref={routeHref("/import")}
            emptyAction="Import text"
            onRetry={() => void loadImports(true)}
          >
            <div class="session-list">
              <For each={imports()}>
                {(story) => (
                  <ImportedStoryRow
                    story={story}
                    busyAction={busyAction()}
                    onDelete={deleteImported}
                  />
                )}
              </For>
            </div>
            <Show when={importsHasMore()}>
              <div class="load-more-row">
                <button class="secondary-button" type="button" disabled={importsLoadingMore()} onClick={() => void loadImports(false)}>
                  {importsLoadingMore() ? "Loading..." : "Load more"}
                </button>
              </div>
            </Show>
          </LibrarySectionState>
        </section>

        <section class="library-section" aria-labelledby="library-generated">
          <header class="library-section-heading">
            <div>
              <h2 id="library-generated">Generated stories</h2>
              <p>{sessions().length} shown</p>
            </div>
            <div class="segmented-control compact-segmented">
              <button type="button" aria-pressed={!generatedArchived()} onClick={() => setGeneratedArchived(false)}>
                Active
              </button>
              <button type="button" aria-pressed={generatedArchived()} onClick={() => setGeneratedArchived(true)}>
                Archived
              </button>
            </div>
          </header>
          <LibrarySectionState
            loading={sessionsLoading()}
            error={sessionsError()}
            empty={sessions().length === 0}
            loadingText="Loading generated stories..."
            emptyTitle={generatedArchived() ? "No archived sessions" : "No sessions yet"}
            emptyText={generatedArchived() ? "Archived sessions will appear here." : "Start a session to generate your first story and tasks."}
            emptyHref={routeHref("/start")}
            emptyAction={generatedArchived() ? "" : "Start session"}
            onRetry={() => void loadSessionsPage(true)}
          >
            <div class="session-list">
              <For each={sessions()}>
                {(session) => (
                  <LibrarySessionRow
                    session={session}
                    retrying={retryingSessionID() === session.session_id}
                    archivedView={generatedArchived()}
                    busyAction={busyAction()}
                    onRetry={retryFailedSession}
                    onArchive={archiveCurrentSession}
                    onRestore={restoreSession}
                    onDelete={deleteGenerated}
                  />
                )}
              </For>
            </div>
            <Show when={sessionsHasMore()}>
              <div class="load-more-row">
                <button class="secondary-button" type="button" disabled={sessionsLoadingMore()} onClick={() => void loadSessionsPage(false)}>
                  {sessionsLoadingMore() ? "Loading..." : "Load more"}
                </button>
              </div>
            </Show>
          </LibrarySectionState>
        </section>
      </div>

      <ConfirmDialog request={confirmRequest()} onCancel={() => setConfirmRequest(null)} />
    </section>
  );
}

function ImportedStoryRow(props: {
  story: ImportedStory;
  busyAction: string;
  onDelete: (story: ImportedStory) => Promise<void>;
}) {
  const href = () => readerHref(props.story.story_id);
  const open = () => {
    window.location.hash = href();
  };

  return (
    <article
      class="session-row library-row imported-row"
      data-openable="true"
      role="link"
      tabindex={0}
      onClick={(event) => {
        if (!isInteractiveTarget(event.target)) {
          open();
        }
      }}
      onKeyDown={(event) => {
        if (isInteractiveTarget(event.target)) {
          return;
        }
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          open();
        }
      }}
    >
      <div class="session-row-main">
        <div class="session-title-line">
          <h3>{props.story.title}</h3>
          <span class="status-chip">Imported</span>
        </div>
        <dl class="session-meta" aria-label="Imported text metadata">
          <div>
            <dt>Language</dt>
            <dd>{props.story.language.toUpperCase()}</dd>
          </div>
          <div>
            <dt>Level</dt>
            <dd>{formatLevel(props.story.level)}</dd>
          </div>
          <div>
            <dt>Created</dt>
            <dd>{formatUnixSeconds(props.story.created_at)}</dd>
          </div>
        </dl>
        <p class="session-detail-line">Open in reader.</p>
      </div>
      <div class="library-row-actions">
        <RowMenu label={`Actions for ${props.story.title}`}>
          <button
            class="danger-menu-item"
            type="button"
            disabled={props.busyAction === `delete:import:${props.story.story_id}`}
            onClick={() => void props.onDelete(props.story)}
          >
            {props.busyAction === `delete:import:${props.story.story_id}` ? "Deleting..." : "Delete"}
          </button>
        </RowMenu>
      </div>
    </article>
  );
}

function LibrarySectionState(props: {
  loading: boolean;
  error: string;
  empty: boolean;
  loadingText: string;
  emptyTitle: string;
  emptyText: string;
  emptyHref: string;
  emptyAction: string;
  onRetry: () => void;
  children: JSX.Element;
}) {
  return (
    <Switch>
      <Match when={props.loading}>
        <div class="home-state" aria-busy="true">{props.loadingText}</div>
      </Match>
      <Match when={props.error}>
        <div class="home-state inline-error" role="alert">
          <p>{props.error}</p>
          <button class="secondary-button" type="button" onClick={props.onRetry}>
            Retry
          </button>
        </div>
      </Match>
      <Match when={props.empty}>
        <div class="home-state empty-state">
          <h3>{props.emptyTitle}</h3>
          <p>{props.emptyText}</p>
          <Show when={props.emptyAction}>
            <a class="button-link secondary-link" href={props.emptyHref}>{props.emptyAction}</a>
          </Show>
        </div>
      </Match>
      <Match when={!props.empty}>
        {props.children}
      </Match>
    </Switch>
  );
}

function libraryListErrorMessage(kind: "imports" | "sessions", error: unknown): string {
  if (error instanceof APIError && error.status === 401) {
    return kind === "imports" ? "Sign in again to load imported texts." : "Sign in again to load generated stories.";
  }
  return kind === "imports" ? "Imported texts could not be loaded." : "Generated stories could not be loaded.";
}

function retrySessionErrorMessage(error: unknown): string {
  if (error instanceof APIError && error.status === 503) {
    return "Generation is not configured. Start the gateway before retrying.";
  }
  return "The session could not be retried.";
}

function importedActionErrorMessage(error: unknown): string {
  if (error instanceof APIError && error.status === 404) {
    return "That imported text is no longer available.";
  }
  return "The imported text could not be deleted.";
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

function isInteractiveTarget(target: EventTarget | null): boolean {
  return target instanceof Element && Boolean(target.closest("a,button,input,select,textarea,[role='menu']"));
}
