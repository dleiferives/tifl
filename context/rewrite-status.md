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

## Next (rough order)

1. **DB layer** — SQLite `Repository` impl + an embedded migration runner
   (`go:embed migrations/*.sql`); wire `Migrate()` into `cmd/server` startup.
   Then a Postgres impl behind the same interface.
2. **Greek language plugin** (`internal/lang/grc`) — the reference implementation:
   tokenization, LLM-backed key resolution (cache in `story_tokens`), item types,
   frequency list. See `context/language-plugins.md` ("Greek: The Reference
   Implementation").
3. **LLM gateway** — real OpenAI-compatible `/v1/chat/completions` with provider
   routing + retry + `llm_calls` logging; `internal/llm` gateway client.
4. **Selection layer** + **algorithmic predictor** — the hard system.
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
