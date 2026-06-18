# Auth & User Management

_Status: active design notes_

## The Problem

The system is moving from a self-hosted single-user tool toward a commercial
multi-tenant SaaS product. That shift requires auth, per-user data isolation,
and a deployment model that works across cloud, desktop-local, and mobile.
The auth design must not add friction for local/offline use while still being
solid for the cloud product.

---

## Auth Strategy

### Why not a managed provider

Auth0, Clerk, and similar services solve real problems but introduce a third-party
dependency that sits between users and their data. For a commercial language
learning product where the user's progress history is the core asset, that
dependency is a liability. It also adds recurring cost per user that cuts into
margins, and makes self-hosting more complex.

Rolling auth in Go is roughly 300 lines — password hashing, token generation,
token validation, refresh. It is not the interesting part of this system and it
is not risky to own.

### Password hashing

**argon2id** via Go's `golang.org/x/crypto/argon2` package. Preferred over bcrypt
because it is memory-hard (resists GPU cracking) and is the current OWASP
recommendation. Bcrypt is acceptable as a fallback but should not be the first
choice for new systems.

Parameters: time=1, memory=64MB, threads=4, key length=32 bytes. These are the
current recommended defaults and should be stored alongside the hash so they can
be increased without invalidating existing hashes.

### Token model

Two tokens per authenticated session:

**Access token** — short-lived JWT (15 minutes). Stateless. Contains: `user_id`,
`email`, `issued_at`, `expires_at`. Validated by the server on every protected
request by checking the signature and expiry. No database lookup required.

**Refresh token** — long-lived opaque token (30 days). Stored in the database
(`refresh_tokens` table) and in an httpOnly, Secure, SameSite=Strict cookie on
the client. On expiry of the access token, the client presents the refresh token
to get a new access token. The refresh token is rotated on each use (old one
invalidated, new one issued) — this limits the window of stolen token abuse.

httpOnly cookies are non-negotiable for the refresh token. JavaScript cannot
read httpOnly cookies, which eliminates the XSS attack surface for the
long-lived credential.

### Auth endpoints

```
POST /api/v1/auth/register     { email, password } → { access_token, user }
POST /api/v1/auth/login        { email, password } → { access_token, user }
POST /api/v1/auth/refresh      (cookie) → { access_token }
POST /api/v1/auth/logout       (cookie) → invalidates refresh token
GET  /api/v1/auth/me           (access token) → { user }
```

The access token is returned in the response body (not a cookie) so the SolidJS
client can store it in memory and attach it as a `Bearer` header. Storing the
access token in memory (not localStorage) means it is lost on page refresh —
which is fine, the refresh token cookie survives and the client silently fetches
a new access token on startup.

### Future: Google OAuth

Adding Google OAuth later means adding:
- `POST /api/v1/auth/google` — receives a Google ID token from the client,
  verifies it against Google's public keys, creates or retrieves the user record,
  issues the same JWT+refresh token pair as email/password login

No changes to the token model, the middleware, or the data isolation layer.
OAuth is a registration/login path, not a different auth system.

---

## Multi-Tenancy

Every table that holds user-specific data has a `user_id TEXT NOT NULL` column.
Every query that reads or writes user data includes `WHERE user_id = ?` with the
value extracted from the JWT claim. No user can ever read or write another user's
data — the filter is applied at the repository layer, not the handler layer, so
it cannot be accidentally omitted in a new handler.

The `user_id` is a UUID generated at registration. It is the primary key of the
`users` table and the foreign key on every data table.

```
users
    user_id      TEXT PRIMARY KEY   -- UUID, e.g. "usr_a1b2c3..."
    email        TEXT NOT NULL UNIQUE
    password_hash TEXT              -- null if OAuth-only user
    created_at   REAL NOT NULL
    last_login   REAL

refresh_tokens
    token_hash   TEXT PRIMARY KEY   -- argon2id hash of the opaque token
    user_id      TEXT NOT NULL
    issued_at    REAL NOT NULL
    expires_at   REAL NOT NULL
    revoked      INTEGER NOT NULL DEFAULT 0
```

All other tables (`stories`, `sessions`, `tasks`, `user_knowledge`, etc.) carry
`user_id` and are never queried without it.

---

## Deployment Modes and Auth

The same Go binary handles all deployment modes. Auth behavior is config-driven.

### Cloud / web

Full auth. JWT middleware active on all `/api/v1/` routes except the auth
endpoints themselves. Refresh tokens stored in Postgres. User data isolated by
`user_id` from JWT.

### Mobile (Capacitor)

Identical to cloud/web. The Capacitor webview calls the same cloud API. The
httpOnly cookie works in the webview. No special handling needed.

### Desktop — local mode (no account)

Auth middleware is disabled. A synthetic `user_id = "local"` is injected at the
middleware layer before the request reaches any handler. The handler and
repository layers are unaware they are running in local mode — they see a
`user_id` just like any other request. SQLite is the storage backend.

This enables full functionality (reading, tasks, knowledge tracking) without
requiring the user to create an account. It is the basis for selling the desktop
app as a standalone one-time purchase.

### Desktop — synced mode (account + local SQLite)

The desktop app can optionally authenticate against the cloud API. When it does,
it uses the real `user_id` from the JWT and writes to local SQLite. A background
sync process periodically pushes local changes to the cloud Postgres. The sync
protocol is an open design question (see below).

---

## User Profile

Beyond auth credentials, each user has a profile that drives the learning system:

```
user_profiles
    user_id          TEXT PRIMARY KEY
    display_name     TEXT
    active_language  TEXT    -- e.g. "el" (Greek)
    level            TEXT    -- e.g. "beginner", "intermediate"
    ui_language      TEXT    -- the user's native language, for glosses
    created_at       REAL
    updated_at       REAL
```

The `active_language` and `level` feed into the selection layer and prompt
builders as part of `LearnerCtx`. These can change (user switches language or
the system promotes them a level) without any auth implications.

---

## Security Considerations

**HTTPS everywhere.** The Go server in cloud mode must run behind TLS. The httpOnly
cookie's `Secure` flag requires HTTPS to be present. Local desktop mode runs on
localhost where this is not a concern.

**Rate limiting on auth endpoints.** Registration and login endpoints must be rate
limited to prevent credential stuffing and brute force. A simple in-memory
rate limiter keyed by IP is sufficient to start. Later: distributed rate limiting
if the cloud deployment scales horizontally.

**No password in logs.** The handler must strip credentials before any logging
occurs. Go's standard `slog` package makes this straightforward with custom log
value handling.

**Token revocation.** The access token is stateless and cannot be individually
revoked before it expires (15 minutes). This is an accepted tradeoff. For
logout, the refresh token is revoked in the database, which prevents the issuance
of new access tokens. An active access token lives at most 15 minutes after
logout — acceptable for this use case.

---

## Open Questions

- **Sync protocol** for desktop-local → cloud. The simplest approach is last-write-wins
  on `user_knowledge` (keyed by `user_id + item_id + updated_at`). Conflicts are
  rare since the same user rarely edits the same item on two devices simultaneously.
  Needs more thought for `conversations` and `tasks` which are append-only.

- **Account deletion.** GDPR and similar regulations require the ability to delete
  all user data. The `user_id` foreign key on every table makes a cascading delete
  straightforward, but it needs to be explicitly implemented and tested.

- **Email verification.** Not required for v1 but should be added before public
  launch to prevent abuse and enable password reset.

- **Password reset flow.** Requires email sending (transactional email provider).
  Out of scope for v1 but the `users` table schema should accommodate it (the
  `email` column is already there).
