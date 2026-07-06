import { createMemo, createSignal, For, Match, onMount, Show, Switch, type JSX } from "solid-js";
import {
  adminCostRollup,
  adminGetCall,
  adminGetSession,
  adminGetUser,
  adminListCalls,
  APIError,
  type APISchema,
} from "../api";
import { routeHref } from "../router";
import { appStore } from "../store";
import {
  formatCallCost,
  formatOptionalTime,
  formatUSD,
  LLMCallRow,
  Metric,
  SessionDebugContent,
} from "./debug";

type CallLogRow = APISchema<"LLMCallLogRow">;
type AdminCallLog = APISchema<"AdminCallLog">;
type CostBucket = APISchema<"CostBucket">;
type AdminCostRollup = APISchema<"AdminCostRollup">;
type AdminUserDetail = APISchema<"AdminUserDetail">;
type LLMCall = APISchema<"LLMCall">;

const PAGE_SIZE = 50;

// AdminShell wraps every admin page with a shared sub-navigation. Reaching any
// of these routes already required the admin flag (main.tsx gates them), so the
// shell assumes access.
function AdminShell(props: { active: string; children: JSX.Element }) {
  const tab = (path: string, label: string, key: string) => (
    <a href={routeHref(path)} aria-current={props.active === key ? "page" : undefined}>{label}</a>
  );
  return (
    <section class="admin-view">
      <header class="view-heading">
        <div>
          <h1>Admin</h1>
          <p>Read-only observability</p>
        </div>
        <nav class="admin-subnav" aria-label="Admin navigation">
          {tab("/admin", "Call log", "calls")}
          {tab("/admin/cost", "Cost", "cost")}
        </nav>
      </header>
      {props.children}
    </section>
  );
}

export function AdminCallLogView() {
  return (
    <AdminShell active="calls">
      <LookupBar />
      <CallLog />
    </AdminShell>
  );
}

export function AdminCostView() {
  return (
    <AdminShell active="cost">
      <CostDashboard />
    </AdminShell>
  );
}

// LookupBar jumps to a session's or user's admin detail page.
function LookupBar() {
  const [sessionId, setSessionId] = createSignal("");
  const [userId, setUserId] = createSignal("");
  const go = (path: string) => { window.location.hash = routeHref(path); };
  return (
    <div class="admin-lookup">
      <form
        onSubmit={(e) => { e.preventDefault(); if (sessionId().trim()) go(`/admin/session/${encodeURIComponent(sessionId().trim())}`); }}
      >
        <label>
          Session lookup
          <input type="text" value={sessionId()} onInput={(e) => setSessionId(e.currentTarget.value)} placeholder="session_id" />
        </label>
        <button class="secondary-button" type="submit">Open</button>
      </form>
      <form
        onSubmit={(e) => { e.preventDefault(); if (userId().trim()) go(`/admin/user/${encodeURIComponent(userId().trim())}`); }}
      >
        <label>
          User lookup
          <input type="text" value={userId()} onInput={(e) => setUserId(e.currentTarget.value)} placeholder="user_id or email" />
        </label>
        <button class="secondary-button" type="submit">Open</button>
      </form>
    </div>
  );
}

interface CallFilters {
  user_id: string;
  model: string;
  kind: string;
  status: string;
  prompt_version: string;
}

function CallLog() {
  const [filters, setFilters] = createSignal<CallFilters>({ user_id: "", model: "", kind: "", status: "", prompt_version: "" });
  const [rows, setRows] = createSignal<CallLogRow[]>([]);
  const [offset, setOffset] = createSignal(0);
  const [hasMore, setHasMore] = createSignal(false);
  const [loading, setLoading] = createSignal(false);
  const [error, setError] = createSignal("");

  const load = async (nextOffset: number) => {
    setLoading(true);
    setError("");
    const finish = appStore.beginOperation();
    try {
      const f = filters();
      const result: AdminCallLog = await adminListCalls({
        user_id: f.user_id || undefined,
        model: f.model || undefined,
        kind: f.kind || undefined,
        status: (f.status || undefined) as "success" | "error" | "timeout" | undefined,
        prompt_version: f.prompt_version || undefined,
        limit: PAGE_SIZE,
        offset: nextOffset,
      });
      setRows(result.calls ?? []);
      setOffset(result.offset);
      setHasMore(result.has_more);
    } catch (err) {
      setError(err instanceof APIError ? `Request failed (${err.status}).` : "Could not load the call log.");
    } finally {
      finish();
      setLoading(false);
    }
  };

  onMount(() => void load(0));

  const update = (key: keyof CallFilters) => (e: { currentTarget: HTMLInputElement | HTMLSelectElement }) =>
    setFilters((prev) => ({ ...prev, [key]: e.currentTarget.value }));

  return (
    <section class="admin-section" aria-label="Call log">
      <form
        class="admin-filters"
        onSubmit={(e) => { e.preventDefault(); void load(0); }}
      >
        <input type="text" value={filters().user_id} onInput={update("user_id")} placeholder="user_id" aria-label="Filter by user" />
        <input type="text" value={filters().model} onInput={update("model")} placeholder="model" aria-label="Filter by model" />
        <input type="text" value={filters().kind} onInput={update("kind")} placeholder="kind" aria-label="Filter by kind" />
        <input type="text" value={filters().prompt_version} onInput={update("prompt_version")} placeholder="prompt_version" aria-label="Filter by prompt version" />
        <select value={filters().status} onChange={update("status")} aria-label="Filter by status">
          <option value="">any status</option>
          <option value="success">success</option>
          <option value="error">error</option>
          <option value="timeout">timeout</option>
        </select>
        <button class="primary-button" type="submit">Apply</button>
      </form>

      <Switch>
        <Match when={loading()}>
          <div class="home-state" aria-busy="true">Loading calls...</div>
        </Match>
        <Match when={error()}>
          <div class="home-state" role="alert">
            <p>{error()}</p>
            <button class="secondary-button" type="button" onClick={() => void load(offset())}>Retry</button>
          </div>
        </Match>
        <Match when={rows().length === 0}>
          <p class="debug-empty">No calls match these filters.</p>
        </Match>
        <Match when={rows().length > 0}>
          <div class="admin-table-scroll">
            <table class="admin-table">
              <thead>
                <tr>
                  <th>Called</th><th>Kind</th><th>Model</th><th>Prompt</th>
                  <th>Status</th><th>Tokens</th><th>Cost</th><th>User</th><th></th>
                </tr>
              </thead>
              <tbody>
                <For each={rows()}>
                  {(row) => <CallRow row={row} />}
                </For>
              </tbody>
            </table>
          </div>
          <div class="admin-pager">
            <button class="secondary-button" type="button" disabled={offset() === 0} onClick={() => void load(Math.max(0, offset() - PAGE_SIZE))}>Previous</button>
            <span>Rows {offset() + 1}–{offset() + rows().length}</span>
            <button class="secondary-button" type="button" disabled={!hasMore()} onClick={() => void load(offset() + PAGE_SIZE)}>Next</button>
          </div>
        </Match>
      </Switch>
    </section>
  );
}

// CallRow is one call-log line; expanding it lazily fetches the full call
// (payloads included) via the per-call detail endpoint and renders it with the
// shared LLMCallRow so list and detail never diverge.
function CallRow(props: { row: CallLogRow }) {
  const [open, setOpen] = createSignal(false);
  const [detail, setDetail] = createSignal<LLMCall | null>(null);
  const [detailError, setDetailError] = createSignal("");

  const toggle = async () => {
    const next = !open();
    setOpen(next);
    if (next && !detail()) {
      const finish = appStore.beginOperation();
      try {
        setDetail(await adminGetCall(props.row.call_id));
      } catch {
        setDetailError("Could not load call detail.");
      } finally {
        finish();
      }
    }
  };

  const tokens = () => `${props.row.input_tokens ?? 0}/${props.row.output_tokens ?? 0}`;

  return (
    <>
      <tr class="admin-call-row" data-status={props.row.status}>
        <td>{formatOptionalTime(props.row.called_at)}</td>
        <td>{props.row.kind}</td>
        <td>{props.row.model}</td>
        <td>{props.row.prompt_version}</td>
        <td><span class="status-chip" data-status={props.row.status}>{props.row.status}</span></td>
        <td>{tokens()}</td>
        <td>{formatCallCost(props.row.cost_usd, props.row.cost_known)}</td>
        <td><code>{props.row.user_id || "—"}</code></td>
        <td><button class="link-button" type="button" onClick={() => void toggle()}>{open() ? "Hide" : "Detail"}</button></td>
      </tr>
      <Show when={open()}>
        <tr class="admin-call-detail">
          <td colSpan={9}>
            <Switch>
              <Match when={detailError()}><p role="alert">{detailError()}</p></Match>
              <Match when={detail()}>{(d) => <LLMCallRow call={d()} />}</Match>
              <Match when={true}><p aria-busy="true">Loading detail...</p></Match>
            </Switch>
          </td>
        </tr>
      </Show>
    </>
  );
}

function CostDashboard() {
  const [rollup, setRollup] = createSignal<AdminCostRollup | null>(null);
  const [loading, setLoading] = createSignal(true);
  const [error, setError] = createSignal("");

  const load = async () => {
    setLoading(true);
    setError("");
    const finish = appStore.beginOperation();
    try {
      setRollup(await adminCostRollup({}));
    } catch (err) {
      setError(err instanceof APIError ? `Request failed (${err.status}).` : "Could not load cost data.");
    } finally {
      finish();
      setLoading(false);
    }
  };

  onMount(() => void load());

  const buckets = createMemo(() => rollup()?.buckets ?? []);

  return (
    <section class="admin-section" aria-label="Cost dashboard">
      <Switch>
        <Match when={loading()}>
          <div class="home-state" aria-busy="true">Loading cost data...</div>
        </Match>
        <Match when={error()}>
          <div class="home-state" role="alert">
            <p>{error()}</p>
            <button class="secondary-button" type="button" onClick={() => void load()}>Retry</button>
          </div>
        </Match>
        <Match when={rollup()}>
          {(data) => (
            <>
              <div class="debug-summary">
                <Metric label="Total cost" value={formatSummary(data())} />
                <Metric label="Buckets" value={buckets().length} />
              </div>
              <Show when={buckets().length > 0} fallback={<p class="debug-empty">No spend recorded for this window.</p>}>
                <div class="admin-table-scroll">
                  <table class="admin-table">
                    <thead>
                      <tr><th>Day</th><th>Model</th><th>Calls</th><th>Input</th><th>Output</th><th>Cost</th></tr>
                    </thead>
                    <tbody>
                      <For each={buckets()}>
                        {(bucket) => <CostRow bucket={bucket} />}
                      </For>
                    </tbody>
                  </table>
                </div>
              </Show>
            </>
          )}
        </Match>
      </Switch>
    </section>
  );
}

function CostRow(props: { bucket: CostBucket }) {
  const b = props.bucket;
  return (
    <tr>
      <td>{b.day || "—"}</td>
      <td>{b.model || "—"}</td>
      <td>{b.calls}</td>
      <td>{b.input_tokens}</td>
      <td>{b.output_tokens}</td>
      <td>{formatCallCost(b.cost_usd, b.cost_known)}</td>
    </tr>
  );
}

function formatSummary(rollup: AdminCostRollup): string {
  const base = formatUSD(rollup.total.total_usd);
  return rollup.total.has_unknown ? `${base}+ (some unknown)` : base;
}

// AdminSessionView renders any user's session debug payload through the shared
// SessionDebugContent, fetched via the admin cross-user endpoint.
export function AdminSessionView(props: { sessionId: string }) {
  const [debug, setDebug] = createSignal<APISchema<"SessionDebug"> | null>(null);
  const [loading, setLoading] = createSignal(true);
  const [error, setError] = createSignal("");

  const load = async () => {
    setLoading(true);
    setError("");
    const finish = appStore.beginOperation();
    try {
      setDebug(await adminGetSession(props.sessionId));
    } catch (err) {
      setError(err instanceof APIError && err.status === 404 ? "Session not found." : "Could not load session.");
    } finally {
      finish();
      setLoading(false);
    }
  };

  onMount(() => void load());

  return (
    <section class="admin-view debug-view">
      <header class="view-heading">
        <div>
          <h1>Session (admin)</h1>
          <p><code>{props.sessionId}</code></p>
        </div>
        <div class="view-heading-actions">
          <a class="button-link secondary-link" href={routeHref("/admin")}>Call log</a>
        </div>
      </header>
      <Switch>
        <Match when={loading()}><div class="home-state" aria-busy="true">Loading...</div></Match>
        <Match when={error()}>
          <div class="home-state" role="alert">
            <p>{error()}</p>
            <button class="secondary-button" type="button" onClick={() => void load()}>Retry</button>
          </div>
        </Match>
        <Match when={debug()}>{(data) => <SessionDebugContent debug={data()} />}</Match>
      </Switch>
    </section>
  );
}

export function AdminUserView(props: { userId: string }) {
  const [detail, setDetail] = createSignal<AdminUserDetail | null>(null);
  const [loading, setLoading] = createSignal(true);
  const [error, setError] = createSignal("");

  const load = async () => {
    setLoading(true);
    setError("");
    const finish = appStore.beginOperation();
    try {
      setDetail(await adminGetUser(props.userId));
    } catch (err) {
      setError(err instanceof APIError && err.status === 404 ? "User not found." : "Could not load user.");
    } finally {
      finish();
      setLoading(false);
    }
  };

  onMount(() => void load());

  return (
    <section class="admin-view">
      <header class="view-heading">
        <div>
          <h1>User (admin)</h1>
          <p><code>{props.userId}</code></p>
        </div>
        <div class="view-heading-actions">
          <a class="button-link secondary-link" href={routeHref("/admin")}>Call log</a>
        </div>
      </header>
      <Switch>
        <Match when={loading()}><div class="home-state" aria-busy="true">Loading...</div></Match>
        <Match when={error()}>
          <div class="home-state" role="alert">
            <p>{error()}</p>
            <button class="secondary-button" type="button" onClick={() => void load()}>Retry</button>
          </div>
        </Match>
        <Match when={detail()}>
          {(data) => (
            <>
              <div class="debug-summary">
                <Metric label="Email" value={data().user.email} />
                <Metric label="Sessions" value={data().sessions.length} />
              </div>

              <section class="admin-section" aria-label="Cost by model">
                <h2>Cost by model</h2>
                <CostTable buckets={data().cost_by_model} label="Model" pick={(b) => b.model || "—"} />
              </section>
              <section class="admin-section" aria-label="Cost by day">
                <h2>Cost by day</h2>
                <CostTable buckets={data().cost_by_day} label="Day" pick={(b) => b.day || "—"} />
              </section>

              <section class="admin-section" aria-label="Sessions">
                <h2>Sessions</h2>
                <Show when={data().sessions.length > 0} fallback={<p class="debug-empty">No sessions.</p>}>
                  <ul class="admin-session-list">
                    <For each={data().sessions}>
                      {(s) => (
                        <li>
                          <a href={routeHref(`/admin/session/${encodeURIComponent(s.session_id)}`)}>
                            <code>{s.session_id}</code>
                          </a>
                          <span>{s.language} · {s.level} · {s.status}</span>
                        </li>
                      )}
                    </For>
                  </ul>
                </Show>
              </section>
            </>
          )}
        </Match>
      </Switch>
    </section>
  );
}

function CostTable(props: { buckets: CostBucket[]; label: string; pick: (b: CostBucket) => string }) {
  return (
    <Show when={props.buckets.length > 0} fallback={<p class="debug-empty">No spend.</p>}>
      <div class="admin-table-scroll">
        <table class="admin-table">
          <thead>
            <tr><th>{props.label}</th><th>Calls</th><th>Input</th><th>Output</th><th>Cost</th></tr>
          </thead>
          <tbody>
            <For each={props.buckets}>
              {(b) => (
                <tr>
                  <td>{props.pick(b)}</td>
                  <td>{b.calls}</td>
                  <td>{b.input_tokens}</td>
                  <td>{b.output_tokens}</td>
                  <td>{formatCallCost(b.cost_usd, b.cost_known)}</td>
                </tr>
              )}
            </For>
          </tbody>
        </table>
      </div>
    </Show>
  );
}
