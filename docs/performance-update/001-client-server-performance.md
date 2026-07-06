# Client/Server Performance Architecture

Status: working design note
Date: 2026-07-06

This note captures the current direction for making tifl feel fast across web,
mobile, and desktop without turning the API into a complicated realtime or sync
platform too early. It is intentionally a planning document. Future performance,
hosting, desktop, mobile, and App Store planning notes should live beside it in
`docs/performance-update/`.

## Goal

The app should feel local even when it is backed by a cloud server:

- Screen transitions should not wait on chains of small API requests.
- Reader interactions should update instantly.
- High-frequency learner signals should not generate one HTTP request per tap or
  keypress.
- Mobile should tolerate bad networks and app backgrounding.
- Desktop should support a true local mode with the same app code.
- The cloud server should stay simple, secure, cheap to host, and scalable.

The target model is:

```text
UI reads local state first
        |
        v
memory cache / IndexedDB / local SQLite
        |
        v
server refreshes and reconciles
```

The server remains authoritative for cloud accounts. The local client cache is a
performance and resilience layer, not an independent source of truth.

## Core Decision

Use a pragmatic mix of:

- BFF-style screen bundles for reads.
- CQRS-lite separation between query/read models and write commands.
- Local read cache for immediate rendering.
- Batched write outbox for high-frequency events.
- Fetch-based SSE streams for long-running generation progress.
- Direct command endpoints for important one-shot actions.

Do not start by adding GraphQL, gRPC-Web, a full local replica protocol, or a
global WebSocket transport.

## What This Means

For web and mobile, the client does not run a local API server. It keeps:

- An in-memory route cache for the active session/page.
- IndexedDB persisted data for useful route bundles and offline-ready artifacts.
- An outbox of pending client events and coalesced commands.

For desktop, the Tauri app can run the real Go API server locally with SQLite.
That is different from the browser/mobile model: desktop local mode can truly be
local API + local DB because the Go sidecar exists on the user's machine.

## Read Path

The current API exposes many useful resource endpoints:

- `GET /api/v1/profile`
- `GET /api/v1/sessions`
- `GET /api/v1/stories/{id}`
- `GET /api/v1/sessions/{id}`
- `GET /api/v1/sessions/{id}/content`
- `GET /api/v1/sessions/{id}/tasks`

Those are still useful as primitives, but a high-performance UI should avoid a
screen depending on a waterfall of those calls.

Add screen-shaped read endpoints:

```http
GET /api/v1/bootstrap
GET /api/v1/home
GET /api/v1/library?cursor=...
GET /api/v1/sessions/{id}/bundle?view=read
GET /api/v1/sessions/{id}/bundle?view=tasks
GET /api/v1/sessions/{id}/bundle?view=review
GET /api/v1/stories/{id}/reader-bundle
```

These endpoints return exactly what a route needs to paint the first useful
screen. They should not include debug payloads, raw LLM calls, or unrelated
future data.

Example `reader-bundle` shape:

```json
{
  "story": {},
  "tokens": [],
  "glossary": [],
  "reader_knowledge": {},
  "surface_levels": {},
  "dictionary_overrides": {},
  "session": null,
  "revision": "story-rev-123"
}
```

Example session read bundle:

```json
{
  "session": {},
  "content": {},
  "tasks_summary": {},
  "stages": [],
  "target_items": [],
  "permissions": {
    "can_read": true,
    "can_open_tasks": true,
    "can_review": false
  },
  "revision": "session-rev-456"
}
```

The frontend route loader should:

1. Look for a matching cached bundle in memory or IndexedDB.
2. Render it immediately if present.
3. Revalidate with the server in the background.
4. Replace only changed local state.
5. Let Solid update only subscribers of changed signals.

## Write Path

Writes split into three classes.

### Immediate Commands

These should call the server directly because the user expects an authoritative
result:

- Login/register/logout.
- Task submit and grade.
- Start generation.
- Retry generation.
- Delete/archive story or session.
- Save/delete dictionary entry.

These can still optimistically update local UI when safe, but they are not
background-only writes.

### Batched Events

These should flow through a local outbox and batch endpoint:

- Reader events.
- Cursor/reading position updates.
- Word lookup signals.
- Peek events.
- Preview guesses.
- Confusion signals.

Potential endpoint:

```http
POST /api/v1/client-events/batch
```

The server should treat event IDs as idempotency keys so retries are safe.

### Coalesced Last-Write-Wins Commands

These should update UI instantly and flush later, with the latest value winning:

- Word/surface knowledge rating for a key.
- Last-read cursor.
- Draft settings edits if any become high-frequency.

Outbox rows need a `merge_key` so repeated updates collapse before flush.

Example outbox record:

```json
{
  "id": "evt_...",
  "kind": "surface_rating",
  "merge_key": "el:word:βλέπω",
  "payload": {
    "language": "el",
    "key": "βλέπω",
    "level": 4
  },
  "idempotency_key": "evt_...",
  "created_at": 1783372800,
  "attempts": 0
}
```

Flush triggers:

- Short debounce window.
- Route change.
- `visibilitychange` when the tab/app is hidden.
- App regains network.
- Before desktop/mobile app shutdown where available.

Use `fetch(..., { keepalive: true })` or `navigator.sendBeacon()` for small
page-exit flushes where appropriate. Keep these payloads small.

## Realtime Path

Keep fetch-based SSE for generation progress. This repo already avoids native
`EventSource` because native `EventSource` cannot attach the bearer token, while
fetch streaming can.

Use SSE/fetch streams for:

- Generation progress.
- Durable job status.
- Possibly long-running import/extraction progress later.

Do not use WebSockets as the default app transport. Add WebSockets only when the
product needs true bidirectional realtime behavior:

- Live tutor chat.
- Speech sessions.
- Collaborative/admin live diagnostics.
- Streaming audio interaction.

## Caching Strategy

### Static Assets

For production web/mobile bundles:

- Fingerprint `main.js` and other static assets.
- Serve with `Cache-Control: public, max-age=31536000, immutable`.
- Serve `index.html` with short cache or revalidation.

The current build outputs `dist/main.js` without a content hash. Production
packaging should change that before a public launch.

### User Data

User-specific API responses should be private:

```http
Cache-Control: private, max-age=0, must-revalidate
ETag: "..."
```

For route bundles, the client can keep its own IndexedDB copy and send:

```http
If-None-Match: "..."
```

The server can return `304 Not Modified` when the bundle revision has not
changed.

### Sensitive Data

Use `Cache-Control: no-store` for:

- Auth responses.
- Raw LLM debug payloads.
- Admin/debug screens.
- Any future billing/payment data.

## Why Not GraphQL First

GraphQL solves a real problem: a client can ask for a graph of related data in
one request. That is useful when many client surfaces need flexible, arbitrary
shapes.

tifl currently has predictable route shapes:

- Home.
- Library.
- Reader.
- Tasks.
- Review.
- Debug.
- Settings.

Screen-bundle endpoints give the main GraphQL performance benefit without
adopting GraphQL's operational costs:

- Resolver tree complexity.
- N+1 query risk.
- New schema/runtime/tooling.
- Harder ordinary HTTP caching.
- More complicated auth and error semantics.
- Another abstraction over the existing OpenAPI contract.

This is not anti-GraphQL. The decision is that explicit screen bundles are the
smaller, more testable, more cacheable solution for this app right now.

If the product later has many independently developed clients asking for
arbitrary data shapes, GraphQL can be reconsidered.

## Why This Is Not Bloat

This becomes bloat if we build a general sync engine before the product needs
one.

This stays pragmatic if we follow these constraints:

- Start with memory cache and route bundles.
- Add IndexedDB only for high-value data: stories, tokens, session bundles,
  task snapshots, dictionary cache, and the outbox.
- Keep the server authoritative.
- Do not duplicate all backend domain logic in the client.
- Do not sync all tables to the browser.
- Do not build conflict resolution until there are real offline multi-device
  writes beyond simple events and last-write-wins preferences.

The goal is local-feeling performance, not a second backend in the browser.

## Security Constraints

Do not store auth tokens in `localStorage`, IndexedDB, or normal Capacitor
preferences.

The current model is right:

- Access token in memory.
- Refresh token in an httpOnly, Secure, SameSite cookie for web.
- Native secure storage for mobile if cookies are not enough in the WebView.
- Desktop local mode can use no auth with synthetic local user.

Offline data is different from auth data. Stories, tokens, tasks, and learning
history may be cached locally, but we need clear product/security choices:

- Logout should clear local user data by default on shared devices.
- "Keep offline library on this device" may be a setting later.
- Imported private texts may require stronger local protection on mobile/desktop.
- Debug payloads should not be cached locally unless explicitly requested.

## Server Implementation Shape

Add bundle handlers as thin HTTP handlers:

```text
handler
  -> repository batch queries
  -> DTO assembler
  -> JSON response with revision/ETag
```

Do not perform LLM calls in bundle handlers. Do not make bundle handlers wait for
background jobs.

Repository work should prefer batch queries:

- Load all tokens for a story once.
- Load reader knowledge map once.
- Load all tasks for a session once.
- Load session stages once.
- Avoid per-token or per-task query loops.

The existing durable jobs system remains the right place for heavy work:

- Generation.
- Reader signal derivation.
- Skill verification.
- Future TTS/import/OCR work.

## Client Implementation Shape

Add a small data layer above `api.ts`:

```text
view
  -> route data loader
  -> memory cache
  -> IndexedDB cache
  -> api.ts typed fetch
```

Suggested modules:

```text
web/src/data/cache.ts
web/src/data/idb.ts
web/src/data/outbox.ts
web/src/data/route_loaders.ts
```

Views should not know whether data came from memory, IndexedDB, or the network.
They should subscribe to route-local Solid signals/stores.

## Measurement Plan

Before changing architecture, establish baselines:

- Requests per route load.
- Total blocking time to first useful render.
- Time to interactive reader.
- Task submit latency.
- Number of reader writes per minute during normal reading.
- Outbox flush success/failure rate.
- Server DB query count per bundle.
- Bundle payload sizes.

Targets:

- Home first useful render: one blocking API request after auth bootstrap.
- Reader first useful render from cache: under 100 ms on normal devices.
- Reader first useful render from network: one route bundle request.
- Word rating UI response: local, effectively instant.
- Word rating network writes: batched/coalesced, not one request per keypress.
- Generation progress: streamed, no polling loop.

## Phased Plan

### Phase 1: Route Bundles

- Add `bootstrap`, `home`, `library`, and session bundle endpoints.
- Keep existing resource endpoints.
- Update views to prefer bundle endpoints where available.
- Measure request count reduction.

### Phase 2: Client Cache

- Add memory cache keyed by route bundle key and revision.
- Add stale-while-revalidate behavior in client code.
- Render cached route data immediately.

### Phase 3: IndexedDB Persistence

- Persist story/session bundles and task snapshots.
- Use IndexedDB for offline reader startup.
- Add cache versioning and invalidation.

### Phase 4: Outbox

- Persist client events and coalesced ratings locally.
- Add batch endpoint and idempotency.
- Flush on debounce, route change, hide, and reconnect.

### Phase 5: HTTP Revalidation

- Add server bundle revisions/ETags.
- Support `If-None-Match` and `304`.
- Add cache-control headers by response class.

### Phase 6: Offline/Desktop/Mobile Sync Design

- Decide how much of this becomes a true sync protocol.
- Desktop local mode can use local Go + SQLite.
- Mobile can cache selected route bundles first, then later add broader offline
  queues if the product needs it.

## Open Questions

- What is the exact first bundle to build: home, reader, or session?
- Should `bootstrap` include profile, languages, feature flags, and auth user?
- What is the canonical revision source for a story bundle?
- What is the canonical revision source for a session bundle?
- How much task data should be cached locally before a task is complete?
- Should dictionary lookup results be cached in IndexedDB, and for how long?
- What local data should logout clear automatically?
- Should mobile require encrypted local storage for imported texts?
- Should we keep a global app event stream later, or only per-job streams?
- When desktop synced mode arrives, do we build our own sync protocol or adopt a
  small purpose-built sync layer?

## Research Anchors

- Microsoft Azure Architecture Center: Backends for Frontends pattern.
- Microsoft Azure Architecture Center: CQRS and queue-based load leveling
  patterns.
- Martin Fowler: CQRS caution and read/write model separation.
- MDN: IndexedDB for large structured client-side data.
- MDN: Service workers as a proxy/cache layer for offline behavior.
- MDN and RFC 9111: HTTP caching, ETags, `If-None-Match`, and
  `stale-while-revalidate`.
- MDN: Server-Sent Events, WebSockets, Fetch streams, `keepalive`, and
  `sendBeacon`.
- Replicache, ElectricSQL, PowerSync, and PouchDB: examples of local-first
  architectures with local reads, mutation queues, and server reconciliation.
- OWASP: avoid sensitive auth material in browser storage; prefer httpOnly
  cookies and secure session handling.
