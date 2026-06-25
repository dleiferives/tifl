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

## Done — step 2: Greek plugin (#5) + selection layer (#6)

- **Language correction**: target language is Modern Greek (`el`, ISO 639-1), not
  Ancient Greek (`grc`). All references updated. Ancient Greek is a future language.
- `internal/lang/el/` — Modern Greek plugin (reference implementation):
  - `Greek` struct implements `lang.Language` fully
  - Tokenizer: Unicode-aware, handles monotonic Greek orthography, NFC normalization,
    apostrophe elision support, reconstructable token sequence
  - `ResolveKey`: v1 normalized-surface approximation (lowercase + strip punctuation);
    TODO spaCy `el_core_news_sm` for true lemmatization
  - `Frequency()`: ~160 Modern Greek lemmas ordered by frequency (particles,
    conjunctions, core verbs, nouns, adjectives, adverbs)
  - `SupportedTaskTypes()`: comprehension_mc, fill_blank, production
  - Tests: Code/RTL/Strategy, Tokenize (surface reconstruction + position sequence),
    ResolveKey (lowercase + strip), word/non-word key rules, frequency list shape
- `lang.Registry.All()` added — used by `seedLanguages` in `cmd/server`
- `cmd/server/main.go` — plugin registration wired: `lang.NewRegistry()` +
  `greekplugin.New()` + `seedLanguages` replaces the old hard-coded seed stub
- `internal/selector/selector.go` — `Selector.Select` now takes `context.Context`;
  `BudgetForLevel` helper (beginner/elementary/intermediate/upper-intermediate/advanced)
- `internal/selector/db_selector.go` — `DBSelector` concrete implementation:
  - Loads all `user_knowledge` + all `knowledge_items` for the language in 2 queries
  - Runs algorithmic predictor over known items
  - Buckets: targets (encountered/recognizing/acquiring, respects `next_target_after`),
    background (acquired/automatic, uniform random shuffle), new (unseen, frequency rank)
  - `ForceTargets` sort first; `ExcludeItems` filtered before bucketing
  - `targetPriority` score: 0.5 × (1−probability) + 0.3 × lookupRatio + 0.2 × stalenessFactor
  - Tests: bucket correctness, exclude filter, empty-knowledge edge case

## Done — reader backend (#10) + acquisition engine (#9)

Landed together on `reader/10-reader-backend` (mutual dependency: #10 produces
reader signals, #9 consumes them).

- **Reader level vs acquisition_stage** — the reader's user-facing knowledge
  *level* (`unseen/1..5/well_known/ignored`, the colour a word is painted) is a
  new `user_knowledge.level` column, deliberately separate from the
  system-computed `acquisition_stage`. Level granularity is per knowledge item
  (lemma for `el`) / per phrase; per-inflection is deferred (#43).
- **Endpoints**: `GET /stories/{id}` (tokens + knowledge in one load),
  `POST /reader/events` (idempotent batch ingest → signal derivation),
  `PUT /word_knowledge/{token}` (optimistic rating), `GET /stories/{id}/definition`
  (glossary → metadata → shared cache → live Wiktionary/LLM), and
  `POST /stories/{id}/sentence` + `/word` (cached LLM breakdowns).
- **`internal/acquire`** — stage-transition evaluator (pure, tunable, tested
  across every documented boundary) + `Engine` that derives `confidence_score`
  (via the predictor — it was never populated before, so acquired→automatic was
  dead) and `acquisition_stage`, no-regress on the hard path. `ApplyTaskGrade`
  is the task-side counterpart (wiring to a grade-submission endpoint is #11).
- **Shared cache**: global `definitions` (source-split: Wiktionary + LLM coexist)
  and `breakdowns` (sentence by normalized-hash, word by key) tables — reused
  across users. Wiktionary is behind an interface, stubbed (kaikki ingestion #41).
- **Deferred → sub-issues**: #40 per-user dictionary, #41 kaikki ingestion,
  #42 structure/phrase caching, #44 wire the
  knowledge_predictions cache (selector still scores on the fly — always fresh).

## Done — reader per-inflection levels (#43)

- Reader self-ratings are now split by scope:
  - ordinary 1–5/w/i ratings write `reader_surface_levels` for the exact displayed
    form (`item_key` + language-owned `surface_key`);
  - explicit lemma/root marks still write canonical `user_knowledge.level` and
    cover all displayed forms in the reader.
- Story tokens carry `surface_key` and the API returns an opaque `form_key` plus
  `surface_knowledge` alongside canonical `knowledge`.

## Done — auth & users (#12)

- JWT cloud mode protects every application API route and injects the validated
  user ID into request context; local mode injects the synthetic `local` user.
- Registration/login, `/auth/me`, refresh rotation, per-device replay revocation,
  logout, and logout-all are implemented.
- Passwords use OWASP-baseline Argon2id. Refresh tokens are opaque 256-bit
  credentials stored as SHA-256 digests in independent device/session families.
- Secure, httpOnly, SameSite=Strict refresh cookies are the default; an explicit
  development switch permits HTTP cookies locally.
- Process-local auth throttling ships for v0.1; email identity hardening (#53)
  and distributed abuse controls (#54) are scheduled for v0.2.0.

## Next (rough order)

1. **Story pipeline** (`internal/story`) — staged, checkpointed generation with
   SSE progress (token-rate ticker); session types.
2. **Task system** — first task types (comprehension MC, fill-blank) + grading.
3. **Reader** — `story_tokens` API + signal logging (lookup/rate).
4. **Web** — api/router/store + reader, home, tasks, settings views.
5. **Shells** — Tauri desktop (Go sidecar), Capacitor mobile.

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
