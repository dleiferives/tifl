# Backend Server Architecture

_Status: active design notes — initial architecture session_

## Why Go, Why Not Python or Node

The previous Python implementation is being discarded entirely. Python is slow,
ships a heavy runtime, and is difficult to distribute as a self-contained binary
for desktop embedding. The replacement is Go because:

- **Single static binary.** The Tauri desktop app bundles the Go server as a
  sidecar. That requires a binary that runs without an interpreter or dependency
  install step. Go compiles to a single executable with no runtime dependencies.
- **Cross-platform compilation.** `GOOS=windows GOARCH=amd64 go build` produces
  a Windows binary from a Linux machine. This matters for distributing the desktop
  app across platforms without platform-specific CI complexity.
- **Performance.** Go is fast enough that the server is never the bottleneck;
  the LLM calls are. But it means the selection layer, signal aggregation, and
  knowledge prediction all run without overhead.
- **No JavaScript server-side.** The user was explicit: Node, Bun, and Deno are
  not on the table. JavaScript runs in the browser. The server is Go.

---

## Project Layout

```
tifl/
├── cmd/
│   ├── server/          # main() for the API server
│   └── gateway/         # main() for the LLM gateway (separate binary)
├── internal/
│   ├── db/              # Repository interface + SQLite/Postgres implementations
│   ├── lang/            # Language plugin registry + per-language packages
│   ├── llm/             # Gateway HTTP client, LearnerCtx, prompt builders
│   ├── reader/          # Tokenization, knowledge lookup, story_tokens
│   ├── story/           # Story generation pipeline
│   ├── tasks/           # Task type registry, task execution, grading
│   ├── selector/        # Item selection layer (hard system boundary)
│   ├── predictor/       # Knowledge probability estimation
│   ├── auth/            # JWT, argon2id, refresh tokens
│   └── handler/         # HTTP handlers — thin, no business logic
├── web/                 # SolidJS frontend source (compiled by esbuild)
├── desktop/             # Tauri shell
├── mobile/              # Capacitor shell
└── spec/                # OpenAPI spec (canonical contract)
```

`cmd/server` and `cmd/gateway` are the only two entrypoints. Everything in
`internal/` is library code — no circular imports, no global state except the
registered plugin tables (language registry, task registry).

---

## The Two Processes

### API Server (`cmd/server`)

The main application. Does everything except talk directly to an LLM provider.

Responsibilities:
- Serve the compiled SolidJS frontend as static files
- Handle all `/api/v1/` JSON endpoints
- Own the database (read/write through the repository interface)
- Run the selection layer before any story or task generation
- Dispatch to the LLM gateway for generation and grading
- Validate JWT tokens and inject `user_id` into every request context
- Run the signal aggregation pipeline after task grading

### LLM Gateway (`cmd/gateway`)

A small, focused HTTP proxy. No database access. No business logic.

Responsibilities:
- Expose an OpenAI-compatible API on a configured local port
- Route inbound requests to the configured upstream provider
  (OpenRouter, Anthropic direct, Ollama, etc.)
- Log every request/response for cost tracking and debugging
- Apply rate limiting and retry logic at this layer, not the app layer

The API server points at the gateway via a single env var: `LLM_BASE_URL`. The
server constructs OpenAI-style requests and sends them there. It has no knowledge
of what is behind the gateway. Swapping providers means changing the gateway's
config; the API server is unaffected.

This separation also means the gateway can be run remotely in the SaaS deployment
(one gateway serving many API server instances) or locally in the desktop
deployment (gateway and server both running as sidecars, talking over localhost).

---

## The Two API Surfaces

The server exposes two distinct surfaces on the same port:

### HTML routes

Served at `/`, `/reader`, `/stories`, etc. Return full HTML pages or partial HTML
fragments. These are for direct browser navigation — the initial page load and
any server-driven navigation. The SolidJS app is loaded from here and then takes
over client-side routing.

In practice these routes are thin: they return the shell HTML that loads the
SolidJS bundle. The SolidJS app does its own routing client-side after that. The
server does not do server-side rendering of component trees.

### JSON API routes (`/api/v1/`)

Versioned, typed JSON. These are what the SolidJS app calls for all data.
They are also what any future third-party client, CLI tool, or mobile native
layer would call. The OpenAPI spec describes these routes exclusively — the HTML
routes are not part of the public contract.

All `/api/v1/` routes are stateless: they accept a JWT, validate it, extract
`user_id`, and operate on that user's data. No server-side sessions.

---

## Repository Interface: Abstracting Storage

The application server never calls `sqlite3` or `pgx` directly. All data access
goes through a `Repository` interface defined in `internal/db/`. Two
implementations exist:

- `SQLiteRepository` — for local/desktop mode. Uses a file-local SQLite database.
  Zero infrastructure dependency. Works offline. Used in the Tauri desktop build.
- `PostgresRepository` — for cloud/SaaS mode. Connects to a managed Postgres
  instance. Supports multiple concurrent users. Used in the hosted deployment.

The implementation is selected at startup based on config. Everything above the
repository layer — handlers, domain logic, the selection layer, the task system —
calls the interface and never knows which storage is underneath.

This is the mechanism that makes the same binary work in both desktop-local and
cloud-SaaS modes.

```
handler
  └─ calls domain logic
       └─ calls Repository interface
            ├─ SQLiteRepository  (local mode)
            └─ PostgresRepository (cloud mode)
```

### Multi-tenancy

In cloud mode, every repository method accepts a `userID string` parameter. Every
query includes `WHERE user_id = $1`. No user can read or write another user's
data. The `userID` is extracted from the validated JWT in the auth middleware and
placed into the request context. Domain logic pulls it from context; it never
arrives as a user-supplied parameter from the HTTP request.

In local/desktop mode with no auth, a synthetic `userID = "local"` is injected
by middleware instead. The repository code is identical in both cases.

---

## Media/Object Storage

Large binary files do not belong in the SQL repository. Generated story audio,
uploaded scan images, speech recordings, and future sidecar files go through the
`ObjectStore` interface in `internal/objectstore`.

Database rows store stable object keys, not provider-specific paths:

```
story_audio/{story_id}/{audio_id}.mp3
task_media/{task_id}/{upload_id}.jpg
conversation_audio/{conversation_id}/{upload_id}.webm
```

The configured object store maps those keys to a backend:

- `local`: files below `media_local_root`, with traversal-resistant key
  resolution.
- `s3`: S3-compatible storage, with short-lived signed URLs by default and
  optional public/CDN URLs through `media_public_base_url`.

Handlers and domain code should validate upload size/type before writing, then
store only the object key in SQL columns such as `story_audio.file_path` or
`tasks.media_path`. API responses should not expose arbitrary local filesystem
paths or accept caller-supplied object keys; media access should go through
domain-scoped routes that return explicit URLs or proxy bytes after auth.

---

## Handler Structure

Handlers are intentionally thin. A handler's job is:

1. Parse and validate the HTTP request (path params, query params, body)
2. Call one or more domain functions from `internal/` packages
3. Serialize the result to JSON (or return an error)

Business logic does not live in handlers. Handlers do not touch the database
directly. This makes domain logic testable without HTTP machinery.

```
POST /api/v1/sessions/generate
  → handler/sessions.go: GenerateSession
      → auth middleware: extract user_id from JWT
      → selector.Select(userID, language, budget)
      → story.Generate(selectedItems, learnerCtx)
      → db.SaveStory(userID, story)
      → db.CreateSession(userID, storyID)
      → tasks.GenerateForStory(story, selectedItems, language)
      → return session_id
```

---

## Auth Middleware

All `/api/v1/` routes except `/api/v1/auth/*` are protected by JWT middleware.

The middleware:
1. Reads the `Authorization: Bearer <token>` header
2. Validates the JWT signature against the server's signing key
3. Checks expiry (access tokens are short-lived, ~15 minutes)
4. Extracts `user_id` from the token claims
5. Places `user_id` into the request context
6. Calls `next`

If validation fails, the middleware returns 401 immediately. The handler never
runs. The handler never sees the token.

Refresh tokens are stored in httpOnly cookies and are longer-lived (~30 days).
The `/api/v1/auth/refresh` endpoint validates the refresh token and issues a new
access token. Refresh tokens are tracked in the database so they can be revoked
on logout.

See `auth-users.md` for the full auth design.

---

## Configuration

The server is configured entirely through environment variables (or a config file
that maps to the same keys). The key config axes are:

| Variable | Values | Effect |
|----------|--------|--------|
| `STORAGE_MODE` | `sqlite`, `postgres` | Which repository implementation to use |
| `DB_PATH` | file path | SQLite file location (sqlite mode only) |
| `DATABASE_URL` | connection string | Postgres DSN (postgres mode only) |
| `LLM_BASE_URL` | URL | Where the LLM gateway is listening |
| `LLM_API_KEY` | string | API key for gateway auth (if any) |
| `AUTH_MODE` | `jwt`, `none` | Disable auth for local desktop mode |
| `JWT_SECRET` | string | Signing key for JWT tokens |
| `FRONTEND_DIR` | path | Where compiled web assets live |

The same binary, different environment, produces different behavior. No
compile-time flags, no build tags. This is what makes the single binary work
across deployment modes.

---

## What the Server Does Not Do

- **No server-side rendering of UI components.** The SolidJS app is compiled
  static assets. The server serves them as files. It does not render HTML from
  component trees.
- **No websockets (initially).** All communication is request/response. The
  reader's optimistic updates are fire-and-forget PUTs. If real-time features
  are needed later (e.g. live audio processing feedback), websockets can be added
  to specific endpoints without changing the overall architecture.
- **No direct LLM provider calls.** All model calls go through the gateway.
- **No background job scheduler (initially).** Signal aggregation and knowledge
  prediction recomputation happen synchronously at the end of task grading. If
  this becomes a latency problem at scale, it moves to an async queue — but that
  is a scaling concern, not a day-one concern.

---

## Open Questions

- Whether the gateway needs its own database for call logging, or whether the
  API server handles that (currently leans toward API server, since it has the
  session context the gateway lacks)
- Exact retry and timeout configuration for gateway → provider calls
- Whether to use `chi`, `echo`, or `net/http` directly for routing (all are
  reasonable; `chi` is lightweight and idiomatic)
