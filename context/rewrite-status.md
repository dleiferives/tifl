# Rewrite status — Python PoC → Go + SolidJS

_Working notes. Updated as the rewrite progresses._

## Decision (2026-06-18)

The Python proof-of-concept (`backend/` FastAPI, vanilla-JS `frontend/`, the
`storylab/` ablation harness, `tests/`) was a "basic idea." We are now building
the **properly-architected** system specified across `context/` — a Go API
server + Go LLM gateway + SolidJS/TypeScript client. The `context/` design docs
are the spec for this build and were kept; everything else was removed.

**Safety net:** the entire pre-rewrite tree is preserved at
- git tag `pre-rewrite-snapshot`
- branch `archive/python-prototype`

(`storylab` — the prompt-pipeline ablation/optimization harness — lives there if
we want to revive or port it.)

## Done — scaffold / walking skeleton

- Removed the Python PoC; kept `context/` and `docs/`.
- Go module `github.com/dleiferives/tifl` (Go 1.26).
- `cmd/server` — runnable: config + router + `/healthz` + `/api/v1/ping` + static
  serving of `web/dist` (placeholder when unbuilt) + graceful shutdown.
- `cmd/gateway` — runnable stub: `/healthz` + `/v1/chat/completions` (501).
- Core interface contracts from the docs, as Go:
  - `internal/domain` — KnowledgeItem, SelectedItems, LearnerCtx, AcquisitionStage
  - `internal/lang` — Language plugin interface + registry, KeyStrategy, Token
  - `internal/selector` — Selector, SelectRequest, Budget
  - `internal/predictor` — KnowledgePredictor, Prediction
  - `internal/tasks` — TaskType interface + registry, Grade
  - `internal/llm` — LLMRequest/Response, PromptBuilder, gateway Client
  - `internal/db` — Repository interface
  - `internal/{reader,story,auth,handler}` — package docs (impl pending)
- `internal/db/migrations/0001_init_sqlite.sql` — full canonical schema
  (all tables + indexes from `context/database-schema.md`).
- `web/` — SolidJS + esbuild scaffold (`build.mjs`, `main.tsx` pinging `/healthz`).
- `spec/openapi.yaml` — contract stub (`/ping`).
- Repo meta: README, CONTRIBUTING, Makefile, `.gitignore`, `.github/workflows/ci.yml`.

## Done — step 1: storage layer

- `internal/db`: pure-Go SQLite (`modernc.org/sqlite`, no cgo) `Repository`
  implementation + an embedded migration runner (`go:embed migrations/*.sql`,
  idempotent, one transaction per file, tracked in `schema_migrations`).
- Wired into `cmd/server` startup: open → migrate → seed `grc` → ensure local
  user. `StorageMode` selects the backend (Postgres returns "not implemented").
- Repository surface implemented + tested: users (incl. dup-email + `ErrNotFound`),
  languages, knowledge items (upsert that COALESCE-preserves frequency), and
  `user_knowledge` round-trip. FK enforcement on; JSON metadata round-trips.
- `internal/id` (UUIDv4); `internal/handler` thin handlers; live DB-backed
  `GET /api/v1/languages`. Tests in `internal/db/sqlite_test.go` (all green).

## Next (rough order)

1. **Postgres `Repository`** behind the same interface (cloud mode) + a fake
   in-memory repo for fast handler tests.
2. **Greek language plugin** (`internal/lang/grc`) — the reference implementation:
   tokenization, LLM-backed key resolution (cache in `story_tokens`), item types,
   frequency list. See `context/language-plugins.md` ("Greek: The Reference
   Implementation").
3. **LLM gateway** — real OpenAI-compatible `/v1/chat/completions` with provider
   routing + retry + `llm_calls` logging; `internal/llm` gateway client.
4. **Selection layer** — the hard system. (Algorithmic predictor ✅ done:
   `internal/predictor`, pure deterministic formula + tests; the selector and a
   repo-backed `KnowledgePredictor` adapter remain.)
5. **Story pipeline** (`internal/story`) — staged, checkpointed generation with
   SSE progress (token-rate ticker); session types.
6. **Task system** — first task types (comprehension MC, fill-blank) + grading.
7. **Reader** — `story_tokens` API + signal logging (lookup/rate).
8. **Auth** — JWT + argon2id (cloud); synthetic `local` user (desktop).
9. **Web** — api/router/store + reader, home, tasks, settings views.
10. **Shells** — Tauri desktop (Go sidecar), Capacitor mobile.

## Open decisions to revisit

- Router: stdlib `net/http` (current, zero-dep) vs `chi` (the docs lean chi).
- SQLite driver: `modernc.org/sqlite` (pure Go, cross-compiles cleanly for the
  Tauri sidecar) vs `mattn/go-sqlite3` (cgo). Pure-Go is the likely pick.
- Plus the per-doc "Open Questions" sections in `context/`.

## Work breakdown → GitHub issues

The remaining work is tracked as ~2-day issues on GitHub
(`dleiferives/tifl`). Each links back to the relevant `context/` sections.

**Core loop — backend (`phase:core-loop`):**
- #3 LLM gateway + `internal/llm` client (provider routing, retry, `llm_calls`)
- #4 Prompt builders + LearnerCtx (story / task / grader / assessor)
- #5 Greek language plugin (`internal/lang/grc`) — reference implementation
- #6 Selection layer + repo-backed predictor adapter + predictions cache
- #7 Story pipeline (staged, checkpointed, SSE progress)
- #8 Task system (`comprehension_mc`, `fill_blank`, grading, `task_targets`)
- #9 Acquisition engine (stage transitions, signal aggregation, invalidation)
- #10 Reader backend (`story_tokens`, `reader_events`, glossary, breakdowns)
- #11 Skill system (XP, tiers, AI verification, level promotion, skill-tree API)
- #12 Auth & users (JWT + refresh, argon2id, middleware, local user)
- #13 Session types (system / topic-guided / expression-guided)
- #14 Postgres repository + in-memory fake repo

**Clients (`phase:clients`):**
- #15 Web foundation (api client, router, store, theming, build)
- #16 Reader UI · #17 Tasks UI · #18 Session start + generation UX
- #19 Skill tree UI · #20 Tauri desktop shell · #21 Capacitor mobile shell

**Post-MVP (`phase:future`):**
- #22 Input modalities (speech / scan / print) · #23 ML predictor + training ·
  #24 Observability & admin

Rough critical path: #3 → #4 → (#5, #6) → #7 → #8 → (#9, #10, #11) → clients.
