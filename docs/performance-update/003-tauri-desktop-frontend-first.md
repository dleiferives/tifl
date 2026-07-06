# Tauri Desktop Frontend-First Plan

Status: working design note
Date: 2026-07-06

This note corrects the earlier desktop direction. The Tauri app is not a
self-hosted app and should not run the real backend locally. The near-term goal
is a packaged desktop frontend that talks to the existing hosted `/api/v1`
backend, with local caching and offline capability added carefully over time.

This supersedes the desktop-local-server assumption in
`001-client-server-performance.md`.

## Current Decision

Use the same hosted backend and the same v1 API for now:

```text
Tauri desktop app
  -> bundled Solid frontend
  -> existing api.ts client
  -> hosted /api/v1 backend
  -> hosted Postgres / jobs / LLM gateway
```

Do not build a local Go sidecar, local API server, local LLM gateway, or local
source-of-truth database for the first desktop version.

The first Tauri version should be mostly a frontend packaging project:

- Reuse the existing Solid app.
- Reuse the existing `/api/v1` API surface.
- Reuse the hosted auth and backend.
- Add only the native features required for a real desktop app.
- Keep offline storage as a scoped client cache, not a second backend.

## Why This Is The Right First Step

The product needs a sellable hosted app. Running the backend locally would create
two products:

- A hosted SaaS app.
- A local/self-hosted desktop app.

That doubles the hardest parts: auth, sync, generation, migrations, support,
debugging, account state, and data recovery. It also makes it harder to know
which behavior is canonical.

The cleaner model is:

```text
Hosted backend = authority.
Desktop app = fast native client.
Local storage = cache plus offline queue.
```

This keeps one source of truth while still letting the desktop app feel fast.

## Near-Term Architecture

### Runtime

The Tauri shell loads bundled web assets from the app package. The bundled Solid
app calls the hosted API over HTTPS.

```text
App launches
  -> Tauri opens the main WebView
  -> Solid app boots from local bundled assets
  -> api.ts uses configured hosted API base URL
  -> UI behaves like the current website
```

The app should not depend on a development server, localhost API, or local
sidecar process in production.

### API

Keep using the old v1 API first:

```text
/api/v1/auth/...
/api/v1/profile
/api/v1/stories
/api/v1/sessions
/api/v1/sessions/{id}/content
/api/v1/sessions/{id}/tasks
/api/v1/reader-events
/api/v1/tasks/...
```

The current API may not be the final performance shape, but it is good enough to
prove the desktop shell and avoid mixing too many migrations at once.

The first Tauri milestone should not require new backend endpoints unless a
small compatibility endpoint is genuinely needed.

### Auth

For the first pass, use the same login flow as the website if possible. The app
can load the normal login UI and receive tokens the same way the browser does.

Longer term, desktop auth should become more native:

- Short-lived access token in memory.
- Refresh/session secret stored in OS-backed secure storage or Tauri Stronghold.
- No refresh token in localStorage.
- Logout clears tokens and can optionally clear local cached content.
- Deep links can support external browser auth later.

If v1 auth currently assumes browser cookies, the desktop app may need a small
adapter in `api.ts` or a Tauri auth wrapper. That is still a frontend/client
integration task, not a backend redesign.

### Networking

Use normal HTTPS to the hosted backend.

For MVP, frontend `fetch` is acceptable because it matches the existing web app.
For later hardening, network calls that involve refresh tokens or background
sync can move behind Rust commands so secrets do not live in the WebView.

Tauri HTTP permissions should be scoped tightly to the production API domains,
not wildcard internet access.

## What Runs Locally

The desktop app can run useful native/local work without becoming a local
server.

Good local responsibilities:

- Opening files selected by the user.
- Reading pasted/imported text.
- Basic text normalization.
- Local cache reads and writes.
- Dictionary pack lookup.
- Outbox queue management.
- Downloading cache packs.
- Verifying downloaded pack checksums.
- Secure token storage.
- Native menus, updater, app lifecycle, and filesystem integration.

Bad local responsibilities for the first version:

- Running the full Go API server.
- Running migrations for the hosted schema locally.
- Becoming the source of truth for user data.
- Running LLM jobs locally.
- Managing provider API keys.
- Implementing a second route/API contract.

## Offline And Cache Direction

The first desktop version can ship without full offline support if needed. But
the design should leave room for it.

The eventual offline-capable model is:

```text
UI renders from local cache
  -> user actions write local pending state
  -> app pushes queued changes when online
  -> backend canonicalizes
  -> app pulls updated state back into cache
```

This is still a hosted app. The local database is a cache and queue.

### Phase 1: Simple Cache

Store only low-risk, easy-to-invalidate data:

- Last successful bootstrap/profile payload.
- Recent session list.
- Recent story/session payloads.
- Reading position.
- Basic app settings.

This can start with browser storage inside the Tauri WebView if that is fastest.
That is enough to make reloads and app startup feel less empty.

### Phase 2: Real Local Store

Move durable desktop cache into SQLite through a narrow Tauri/Rust command layer.

Do not expose raw SQL to frontend app code as the main long-term interface.
Prefer typed commands:

```text
getCachedReaderBundle(contentId)
saveCachedReaderBundle(bundle)
recordLocalReaderEvents(events)
getPendingOutboxStats()
syncNow()
```

SQLite is a strong fit here because it is a normal local application file format,
supports indexes, supports full-text search through FTS5, and can handle this
client-side workload easily.

### Phase 3: Offline Reader

Offline reader support should mean:

- User can open already-downloaded stories.
- User can read and update reading position.
- User can mark known/unknown.
- User can do cached review tasks.
- User can add notes/highlights.
- Changes queue locally.

Offline should not mean:

- Generating new AI stories.
- Running new LLM breakdowns.
- Fetching missing dictionary entries from the server.
- Syncing across devices.
- Creating new backend jobs.

### Phase 4: Offline Imports

Eventually, the user should be able to paste or import their own story while
offline.

The local app can:

- Store the raw text locally.
- Segment it into sections.
- Tokenize enough for reading.
- Lookup words from downloaded dictionary packs.
- Let the user add personal dictionary entries.
- Queue the import for server canonicalization later.

When online, the hosted backend can do the heavier canonical work:

- Persist the imported content.
- Run higher-quality processing.
- Generate breakdowns.
- Create tasks.
- Sync the final artifact to other devices.

## Dictionary Pack Direction

The desktop app should not download all dictionary data for all languages.

It should download language-scoped packs:

```text
packs/
  spanish-dictionary-v12.sqlite
  french-dictionary-v7.sqlite
  japanese-dictionary-v3.sqlite
```

Lookup priority should be:

```text
1. User's personal dictionary entries.
2. App-curated learner dictionary.
3. Downloaded Wiktionary-derived language pack.
4. Online fallback when connected.
```

This supports the user flow we actually care about:

```text
User imports or opens a story
  -> app tokenizes it
  -> app checks personal entries
  -> app checks downloaded language pack
  -> reader can show local definitions
  -> user can add better meanings as they go
```

The backend should eventually build these packs from Wiktionary/Wiktextract or
Kaikki-derived data. The client should download prepared packs, not parse raw
Wiktionary dumps.

Possible pack tables:

```text
dictionary_entries
  entry_id
  language
  lemma
  normalized_lemma
  part_of_speech
  short_gloss
  frequency_rank

surface_forms
  language
  normalized_surface
  entry_id
  form_tags

senses
  entry_id
  sense_index
  gloss
  examples_json

dictionary_fts
  lemma
  gloss
  examples
```

Direct lookup should use indexed normalized surface/lemma columns. FTS should be
used for searching definitions and dictionary browsing, not the primary
per-token reader lookup.

## Native Integration

The Rust side should be used where it actually improves the app:

- Secure token/session storage.
- File picking and safe file reads.
- SQLite cache access.
- Large dictionary pack download and verification.
- Background sync scheduling.
- Native updater.
- Deep links for login/opening content later.

The frontend should continue to own:

- Solid UI rendering.
- Route state.
- User interaction.
- Calling the existing v1 API.
- Most screen-level product logic until there is a strong reason to move it.

The boundary should look like:

```text
Solid UI
  -> api.ts for hosted /api/v1 calls
  -> desktop.ts for optional native capabilities
  -> Rust commands for local storage/files/secrets
```

Not:

```text
Solid UI
  -> local desktop backend
  -> hosted backend
```

## Security Direction

Minimum desktop security baseline:

- Strict Content Security Policy.
- No remote JavaScript.
- HTTPS-only API calls.
- Tauri capabilities scoped to exactly what the app uses.
- HTTP plugin scoped to the hosted API domain if used.
- No wildcard filesystem access.
- Imported story text treated as text, never trusted HTML.
- Refresh/session secrets stored outside localStorage.
- Signed desktop updates.

The WebView should be treated as less trusted than Rust. That is the main reason
to keep secrets, filesystem access, and future SQLite writes behind typed Rust
commands rather than handing broad native permissions to frontend JavaScript.

## Performance Direction

For the first version, performance comes from:

- Bundled local assets instead of loading the website over the network.
- Reusing the existing optimized frontend build.
- Keeping the app open like a native app instead of a browser tab.
- Avoiding backend/API rewrites while validating the desktop product.

After that, performance improves through local cache:

- Render recent screens from cache.
- Sync in the background.
- Avoid spinner-first route loads.
- Keep downloaded stories available offline.
- Use local dictionary packs for instant lookup.
- Batch reader events and sync them later.

The larger screen-bundle and sync API ideas still matter, but they should remain
future work until the desktop shell proves itself against `/api/v1`.

## Future Backend Ideation

The v1 API is the correct near-term constraint. It avoids a rewrite while we
learn what the desktop app actually needs.

Later, if desktop/mobile offline becomes central, add a v2 surface beside v1:

```text
GET  /api/v2/bootstrap
GET  /api/v2/library/bundle
GET  /api/v2/content/{id}/reader-bundle
POST /api/v2/events/batch
POST /api/v2/sync/push
GET  /api/v2/sync/changes?since=...
GET  /api/v2/dictionary-packs
```

That future API should be designed around:

- Fewer screen-load requests.
- Explicit sync cursors.
- Idempotent client events.
- Dictionary pack manifests.
- Cache revisions and ETags.
- Small route-shaped payloads.

But this should not block the first Tauri build.

## Milestones

### Milestone 1: Packaged Desktop Website

- Add Tauri shell.
- Build and bundle existing Solid frontend.
- Configure production API base URL.
- Ensure login works.
- Ensure main current routes work.
- Ship a basic signed desktop build for internal testing.

### Milestone 2: Desktop Polish

- Native window title/menu behavior.
- Remember window size.
- App updater path.
- Error UI for offline/API unavailable states.
- Basic native file picker for story import if the existing API supports it.

### Milestone 3: Local Cache

- Add desktop cache abstraction.
- Cache recent story/session payloads.
- Show cached reader content when offline.
- Clear cache on logout or account switch.

### Milestone 4: Dictionary Packs

- Backend publishes language pack manifests.
- Desktop downloads selected pack.
- Rust verifies checksum.
- Local lookup uses personal entries first, then pack entries.
- Reader definitions work for downloaded content while offline.

### Milestone 5: Offline Queue

- Reader events write locally first.
- Task attempts can queue where safe.
- Sync pushes queued events when online.
- Server handles idempotency.
- Client reconciles canonical state.

### Milestone 6: Better API

- Add bundle/sync endpoints only after the current desktop app shows where v1
  is hurting.
- Migrate one route at a time.
- Keep v1 alive until website, desktop, and mobile have moved.

## Research Anchors

- Tauri commands are the intended typed bridge from frontend code into Rust:
  https://v2.tauri.app/develop/calling-rust/
- Tauri channels are better than events for ordered/high-throughput native-to-UI
  progress:
  https://v2.tauri.app/develop/calling-frontend/
- Tauri's security model treats Rust and WebView code as different trust
  boundaries:
  https://v2.tauri.app/security/
- Tauri CSP helps reduce XSS impact in bundled WebView apps:
  https://v2.tauri.app/security/csp/
- Tauri HTTP permissions can scope allowed network URLs:
  https://v2.tauri.app/reference/javascript/http/
- Tauri Stronghold is available for secret storage patterns:
  https://v2.tauri.app/plugin/stronghold/
- SQLite FTS5 supports local full-text search for dictionary/search use cases:
  https://sqlite.org/fts5.html
- SQLite WAL mode is a common fit for local app databases with concurrent reads:
  https://sqlite.org/wal.html
- Wiktextract/Kaikki can provide prepared Wiktionary-derived dictionary data:
  https://github.com/tatuylonen/wiktextract
  https://kaikki.org/dictionary/rawdata.html
