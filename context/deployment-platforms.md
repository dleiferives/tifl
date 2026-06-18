# Deployment & Client Platforms

_Status: active design notes_

## The Core Principle: One Codebase, Many Shells

The web frontend (SolidJS, TypeScript) and the Go API server are written once.
Every client platform — browser, desktop, mobile — is a shell around these two
artifacts. The shells differ in how they bundle and distribute the code, and in
what storage and auth they use. The application logic is identical.

```
┌──────────────────────────────────────────────────────────┐
│  Shared code                                             │
│  ┌─────────────────────┐  ┌──────────────────────────┐  │
│  │  web/  (SolidJS TS) │  │  Go binary (server+gw)   │  │
│  └─────────────────────┘  └──────────────────────────┘  │
└──────────┬───────────────────────────┬───────────────────┘
           │                           │
    ┌──────▼──────┐   ┌───────────▼──────────┐   ┌────────────────▼──────┐
    │  Browser    │   │  Tauri desktop shell  │   │  Capacitor mobile     │
    │  (web app)  │   │  bundles Go + web/    │   │  shell wraps web/     │
    └─────────────┘   └───────────────────────┘   └───────────────────────┘
```

Config drives which storage backend, auth mode, and LLM routing are used. The
binary does not change between deployments.

---

## Platform 1: Web (Browser)

### What it is
The canonical, cloud-hosted deployment. Users access it through a browser at your
domain. This is the primary commercial product.

### Architecture
- Go server runs on your infrastructure (VPS, container, etc.)
- Serves the compiled SolidJS app as static files from `web/dist/`
- All data stored in PostgreSQL — one database, all users, isolated by `user_id`
- Auth: JWT access tokens + httpOnly refresh token cookies
- LLM requests: Go server → LLM gateway → OpenRouter (or direct provider)

### What the user experiences
A normal web app. They log in, their progress is in the cloud, accessible from
any browser. Nothing to install.

### Deployment considerations
- The Go server and LLM gateway run as separate processes (or containers)
- The gateway is the only process that holds provider API keys
- The app server holds no provider credentials — it only knows the gateway URL
- Static assets (`web/dist/`) can be served from a CDN if needed; the Go server
  can also serve them directly for simplicity

---

## Platform 2: Desktop (Tauri)

### What it is
A native desktop application for macOS, Windows, and Linux. Built with Tauri
(Rust shell). Targets users who want an installable app, offline capability, or
the local-only product tier (one-time purchase, no subscription).

### Architecture

```
┌─────────────────────────────────────────────┐
│  Tauri process (Rust)                        │
│  ┌─────────────────────────────────────────┐│
│  │  WebView                                ││
│  │  Points at http://localhost:PORT        ││
│  │  Renders the same SolidJS web app       ││
│  └─────────────────────────────────────────┘│
│                                              │
│  Sidecar: Go binary                          │
│  ┌──────────────────┐ ┌───────────────────┐ │
│  │  API server      │ │  LLM gateway      │ │
│  │  SQLite storage  │ │  → OpenRouter or  │ │
│  │  no auth (local) │ │     local Ollama  │ │
│  └──────────────────┘ └───────────────────┘ │
└─────────────────────────────────────────────┘
```

Tauri bundles the Go binary as a **sidecar** — an external binary that Tauri
starts on launch and stops on exit. The binary is compiled for each target
architecture (x86_64-apple-darwin, aarch64-apple-darwin, x86_64-pc-windows-msvc,
x86_64-unknown-linux-gnu) and included in the Tauri bundle per Tauri's sidecar
naming convention.

On launch: Tauri starts the Go sidecar, which binds to a random localhost port
and signals readiness. Tauri points its WebView at that port. From the web app's
perspective it is making fetch calls to localhost — identical to the cloud
deployment.

### Local-only mode (no account)
- SQLite file in the OS-appropriate app data directory
- Auth middleware disabled; a synthetic `user_id = "local"` is injected by
  middleware
- All domain logic is identical — the repository interface abstracts storage
- LLM calls go through the bundled gateway, which uses a user-supplied API key
  (entered once in settings, stored in OS keychain) or a local Ollama instance

### Synced mode (with account)
- User authenticates against the cloud
- Local SQLite is used as a cache/queue
- Changes sync to cloud Postgres on connectivity
- Sync protocol: last-write-wins on `updated_at` timestamps for most tables;
  additive-only for event log tables (exposure, task attempts)
- See open questions — sync is not yet designed in detail

### Product tier implications
- **Local-only**: one-time purchase. No server costs. User brings their own API
  key or runs Ollama locally. Fully offline after install.
- **Synced**: subscription. Cloud sync, cross-device, cloud-hosted LLM costs
  covered. Same app binary, different config at login time.

### Why Tauri (not Electron)
- Rust shell is much smaller and faster than a bundled Chromium
- The WebView uses the OS's native renderer (WebKit on macOS, WebView2 on Windows)
- Go binary is already a single static binary — trivial to bundle as a sidecar
- No Node.js required anywhere in the stack

---

## Platform 3: Mobile (Capacitor)

### What it is
iOS and Android apps distributed through the App Store and Play Store.

### Architecture
Capacitor wraps the existing SolidJS web app in a native shell. The web code runs
in a WebView. Capacitor plugins provide access to native APIs (haptics, push
notifications, camera for scan mode, microphone for speech input).

```
┌─────────────────────────────────────────────┐
│  Capacitor native shell (iOS / Android)      │
│  ┌─────────────────────────────────────────┐│
│  │  WebView                                ││
│  │  Runs web/ SolidJS app                  ││
│  │  fetch() calls → cloud API              ││
│  └─────────────────────────────────────────┘│
│  Native plugins:                             │
│  - Microphone (speech input)                 │
│  - Camera (scan printed tasks)               │
│  - Haptics (reader word navigation)          │
│  - Push notifications                        │
│  - Secure storage (JWT tokens)               │
└─────────────────────────────────────────────┘
                    │ HTTPS
         ┌──────────▼─────────┐
         │  Cloud API server  │
         │  (same Go binary)  │
         └────────────────────┘
```

### Why Capacitor (not React Native or Flutter)
- The web app is already written in SolidJS / raw HTML+CSS. Capacitor wraps it
  with zero rewrite — no React, no Dart, no new language.
- Capacitor is framework-agnostic; it does not require or assume React.
- Performance for a content-reading app is entirely adequate from a WebView.
- One codebase (web/) serves browser, Tauri, and Capacitor without modification.

### Mobile-specific concerns
- **Auth**: JWT stored in Capacitor's SecureStorage plugin (not localStorage),
  which maps to iOS Keychain / Android Keystore.
- **Offline reading**: a previously loaded story and its tokens can be cached in
  local SQLite (via Capacitor SQLite plugin) for offline reading. Knowledge level
  updates queue locally and sync when connectivity returns.
- **Speech input**: native microphone access via Capacitor plugin; STT either
  on-device (if available) or via a cloud STT endpoint on the Go server.
- **Scan input**: camera via Capacitor Camera plugin; image uploaded to Go server
  for OCR processing.

---

## Config-Driven Deployment

The same Go binary supports all server-side modes via environment variables or a
config file:

| Variable | Web/cloud | Desktop local | Desktop synced |
|----------|-----------|---------------|----------------|
| `DB_DRIVER` | `postgres` | `sqlite` | `sqlite` |
| `DB_DSN` | postgres connection string | path to `.db` file | path to `.db` file |
| `AUTH_ENABLED` | `true` | `false` | `true` |
| `GATEWAY_URL` | internal gateway URL | `http://localhost:PORT` | cloud gateway URL |
| `SYNC_ENABLED` | `false` | `false` | `true` |
| `SYNC_ENDPOINT` | — | — | cloud API base URL |

No compile-time differences. The binary is built once per target architecture.

---

## The Local → Cloud Migration Path

A desktop user who starts in local-only mode and later creates an account needs
their data migrated to the cloud. The migration path:

1. User authenticates for the first time (creates account or logs in)
2. App detects local SQLite with `user_id = "local"`
3. A one-time migration endpoint (`POST /api/v1/migrate/local`) accepts a dump
   of the local database
4. Server inserts all records under the user's real `user_id`, deduplicating
   by natural keys (story text, item key, etc.)
5. Local SQLite is re-keyed to the real `user_id` and enters sync mode

This is a one-way, one-time operation. After migration the local DB is a sync
cache, not the source of truth.

---

## Distribution

| Platform | Distribution channel | Update mechanism |
|----------|---------------------|-----------------|
| Web | Your domain | Deploy new Go binary + web/dist |
| Desktop macOS | Direct download + Mac App Store (future) | Tauri updater plugin |
| Desktop Windows | Direct download + Microsoft Store (future) | Tauri updater plugin |
| Desktop Linux | Direct download (.deb, .AppImage) | Manual / package manager |
| Mobile iOS | App Store | App Store review + release |
| Mobile Android | Play Store | Play Store release |

---

## Open Questions

- Sync protocol details for desktop synced mode (conflict resolution beyond
  last-write-wins, handling concurrent sessions on multiple devices)
- Whether to support a fully self-hosted server deployment (user runs their own
  Go server + Postgres) — the binary already supports it, just needs docs
- Mac App Store sandboxing constraints on the Go sidecar subprocess
- Offline STT model for mobile (Whisper.cpp is a candidate)
