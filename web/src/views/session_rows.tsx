import { createSignal, onCleanup, onMount, Show, type JSX } from "solid-js";
import type { APISchema } from "../api";
import { routeHref, sessionHref } from "../router";

export type SessionOverview = APISchema<"SessionOverview">;
export type SessionStatus = SessionOverview["status"];

const dateFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
});

export function SessionRow(props: {
  session: SessionOverview;
  retrying: boolean;
  archivedView: boolean;
  busyAction: string;
  onRetry: (sessionID: string) => Promise<void>;
  onArchive: (sessionID: string) => Promise<void>;
  onRestore: (sessionID: string) => Promise<void>;
  onDelete: (session: SessionOverview) => Promise<void>;
}) {
  return (
    <article class="session-row">
      <SessionSummary session={props.session} />
      <div class="session-actions">
        <SessionActions
          session={props.session}
          retrying={props.retrying}
          archivedView={props.archivedView}
          busyAction={props.busyAction}
          onRetry={props.onRetry}
          onArchive={props.onArchive}
          onRestore={props.onRestore}
          onDelete={props.onDelete}
        />
      </div>
    </article>
  );
}

export function LibrarySessionRow(props: {
  session: SessionOverview;
  retrying: boolean;
  archivedView: boolean;
  busyAction: string;
  onRetry: (sessionID: string) => Promise<void>;
  onArchive: (sessionID: string) => Promise<void>;
  onRestore: (sessionID: string) => Promise<void>;
  onDelete: (session: SessionOverview) => Promise<void>;
}) {
  const href = () => sessionPrimaryHref(props.session);
  const open = () => {
    const next = href();
    if (next) {
      window.location.hash = next;
    }
  };
  const onKeyDown: JSX.EventHandlerUnion<HTMLElement, KeyboardEvent> = (event) => {
    if (!href() || isInteractiveTarget(event.target)) {
      return;
    }
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      open();
    }
  };

  return (
    <article
      class="session-row library-row"
      data-openable={href() ? "true" : "false"}
      role={href() ? "link" : undefined}
      tabindex={href() ? 0 : undefined}
      onClick={(event) => {
        if (!isInteractiveTarget(event.target)) {
          open();
        }
      }}
      onKeyDown={onKeyDown}
    >
      <SessionSummary session={props.session} />
      <div class="library-row-actions">
        <Show when={props.session.status === "failed"}>
          <button class="secondary-button compact-button" type="button" disabled={props.retrying} onClick={() => void props.onRetry(props.session.session_id)}>
            {props.retrying ? "Retrying..." : "Retry"}
          </button>
        </Show>
        <Show when={!href() && (props.session.status === "pending" || props.session.status === "generating" || props.session.status === "failed")}>
          <a class="button-link secondary-link compact-link" href={generationHref(props.session.session_id)}>Details</a>
        </Show>
        <RowMenu label={`Actions for ${sessionTitle(props.session)}`}>
          <Show
            when={props.archivedView}
            fallback={
              <button type="button" disabled={isBusy(props.busyAction, "archive", props.session.session_id)} onClick={() => void props.onArchive(props.session.session_id)}>
                {isBusy(props.busyAction, "archive", props.session.session_id) ? "Archiving..." : "Archive"}
              </button>
            }
          >
            <button type="button" disabled={isBusy(props.busyAction, "restore", props.session.session_id)} onClick={() => void props.onRestore(props.session.session_id)}>
              {isBusy(props.busyAction, "restore", props.session.session_id) ? "Restoring..." : "Restore"}
            </button>
          </Show>
          <div class="row-menu-separator" role="separator" />
          <button class="danger-menu-item" type="button" disabled={isBusy(props.busyAction, "delete", props.session.session_id)} onClick={() => void props.onDelete(props.session)}>
            {isBusy(props.busyAction, "delete", props.session.session_id) ? "Deleting..." : "Delete"}
          </button>
        </RowMenu>
      </div>
    </article>
  );
}

function SessionSummary(props: { session: SessionOverview }) {
  return (
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
  );
}

function SessionActions(props: {
  session: SessionOverview;
  retrying: boolean;
  archivedView: boolean;
  busyAction: string;
  onRetry: (sessionID: string) => Promise<void>;
  onArchive: (sessionID: string) => Promise<void>;
  onRestore: (sessionID: string) => Promise<void>;
  onDelete: (session: SessionOverview) => Promise<void>;
}) {
  const session = props.session;
  const readableUserStory = hasReadableUserStory(session);
  return (
    <>
      <Show when={!readableUserStory && (session.status === "pending" || session.status === "generating")}>
        <a class="button-link secondary-link" href={generationHref(session.session_id)}>Generation</a>
      </Show>
      <Show when={session.status === "complete" && !readableUserStory}>
        <a class="button-link" href={reviewHref(session.session_id)}>Review</a>
      </Show>
      <Show when={session.story_id &&
        (session.status === "ready" || session.status === "reading" || readableUserStory)}>
        <a class="button-link" href={readerHref(session.story_id || "", session.session_id)}>
          {session.reading_started_at ? "Continue reading" : "Start reading"}
        </a>
      </Show>
      <Show when={session.content_type === "phrase_set" && (session.status === "ready" || session.status === "reading")}>
        <a class="button-link" href={phrasesHref(session.session_id)}>Phrases</a>
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
      <Show when={session.status === "ready" && !session.story_id && session.content_type !== "phrase_set"}>
        <a class="button-link secondary-link" href={generationHref(session.session_id)}>Generation</a>
      </Show>
      <a class="button-link secondary-link" href={debugHref(session.session_id)}>Debug</a>
      <Show
        when={props.archivedView}
        fallback={
          <button class="secondary-button" type="button" disabled={isBusy(props.busyAction, "archive", session.session_id)} onClick={() => void props.onArchive(session.session_id)}>
            {isBusy(props.busyAction, "archive", session.session_id) ? "Archiving..." : "Archive"}
          </button>
        }
      >
        <button class="secondary-button" type="button" disabled={isBusy(props.busyAction, "restore", session.session_id)} onClick={() => void props.onRestore(session.session_id)}>
          {isBusy(props.busyAction, "restore", session.session_id) ? "Restoring..." : "Restore"}
        </button>
      </Show>
      <button class="danger-button" type="button" disabled={isBusy(props.busyAction, "delete", session.session_id)} onClick={() => void props.onDelete(session)}>
        {isBusy(props.busyAction, "delete", session.session_id) ? "Deleting..." : "Delete"}
      </button>
    </>
  );
}

export function sessionTitle(session: SessionOverview): string {
  if (session.topic) {
    if (session.session_type === "user_added" && session.topic.startsWith("Imported:")) {
      return session.topic.slice("Imported:".length).trim() || "Untitled story";
    }
    return session.topic;
  }
  if (session.session_type === "expression_guided" && session.user_expressions?.length) {
    return session.user_expressions.slice(0, 2).join(", ");
  }
  return `${formatSessionType(session.session_type)} session`;
}

export function sessionPrimaryHref(session: SessionOverview): string {
  // Imported text is durable content. Optional generation work must never make
  // its library row unopenable or redirect a finished story away from reading.
  if (hasReadableUserStory(session)) {
    return readerHref(session.story_id || "", session.session_id);
  }
  if (!(session.status === "ready" || session.status === "reading" || session.status === "complete")) {
    return "";
  }
  if (session.status === "complete") {
    return reviewHref(session.session_id);
  }
  if (session.content_type === "phrase_set") {
    return phrasesHref(session.session_id);
  }
  if (session.story_id) {
    return readerHref(session.story_id, session.session_id);
  }
  return "";
}

export function formatLevel(level: string): string {
  return level.split("-").map((part) => part.charAt(0).toUpperCase() + part.slice(1)).join(" ");
}

export function formatUnixSeconds(value: number): string {
  if (!Number.isFinite(value)) {
    return "Unknown";
  }
  return dateFormatter.format(new Date(value * 1000));
}

export function generationHref(sessionID: string): string {
  return sessionHref(sessionID, "read");
}

export function readerHref(storyID: string, sessionID?: string): string {
  const base = routeHref(`/reader/${encodeURIComponent(storyID)}`);
  if (!sessionID) {
    return base;
  }
  return sessionHref(sessionID, "read");
}

export function phrasesHref(sessionID: string): string {
  return sessionHref(sessionID, "read");
}

export function tasksHref(sessionID: string): string {
  return sessionHref(sessionID, "tasks");
}

export function reviewHref(sessionID: string): string {
  return sessionHref(sessionID, "review");
}

export function debugHref(sessionID: string): string {
  return routeHref(`/debug/${encodeURIComponent(sessionID)}`);
}

export function RowMenu(props: { label: string; children: JSX.Element }) {
  const [open, setOpen] = createSignal(false);
  let root: HTMLDivElement | undefined;

  const closeFromOutside = (event: MouseEvent) => {
    if (root && !root.contains(event.target as Node)) {
      setOpen(false);
    }
  };
  const closeOnEscape = (event: KeyboardEvent) => {
    if (event.key === "Escape") {
      setOpen(false);
    }
  };

  onMount(() => {
    window.addEventListener("click", closeFromOutside);
    window.addEventListener("keydown", closeOnEscape);
  });
  onCleanup(() => {
    window.removeEventListener("click", closeFromOutside);
    window.removeEventListener("keydown", closeOnEscape);
  });

  return (
    <div class="row-menu-wrap" ref={root} onClick={(event) => event.stopPropagation()}>
      <button class="icon-button" type="button" aria-label={props.label} title="Actions" aria-expanded={open()} onClick={() => setOpen(!open())}>
        ...
      </button>
      <Show when={open()}>
        <div class="row-menu" role="menu">
          {props.children}
        </div>
      </Show>
    </div>
  );
}

function sessionDetailLine(session: SessionOverview): string {
  const selected = `${session.selected_counts.targets} targets, ${session.selected_counts.new} new`;
  if (hasReadableUserStory(session) && session.status === "failed") {
    return `${selected}. The story is available; practice-task generation failed.`;
  }
  if (hasReadableUserStory(session) && (session.status === "generating" || session.status === "pending")) {
    return `${selected}. The story is available while practice tasks are generated.`;
  }
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

function hasReadableUserStory(session: SessionOverview): boolean {
  return session.session_type === "user_added" && Boolean(session.story_id);
}

function formatSessionType(sessionType: SessionOverview["session_type"]): string {
  switch (sessionType) {
    case "topic_guided":
      return "Topic-guided";
    case "expression_guided":
      return "Expression-guided";
    case "user_added":
      return "User-added";
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

function isBusy(busyAction: string, kind: string, sessionID: string): boolean {
  return busyAction === `${kind}:${sessionID}`;
}

function isInteractiveTarget(target: EventTarget | null): boolean {
  return target instanceof Element && Boolean(target.closest("a,button,input,select,textarea,[role='menu']"));
}
