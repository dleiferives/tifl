# Contributing

## Toolchain

- **Go** ≥ 1.26 (server + gateway)
- **Node** ≥ 20 + npm (web client; SolidJS via esbuild)
- **just**, **make** — task runners
- **Rust/cargo** — only for the Tauri desktop shell (later)

## Test layers

| Layer | Command | What it covers | Needs |
|-------|---------|----------------|-------|
| Go unit | `make test` | domain logic, selection, predictor, tasks, repository (in-memory / SQLite temp) | nothing |
| Go vet | `make vet` | static checks | nothing |
| Web typecheck | `make web-typecheck` | client TypeScript | `make web-install` |
| Integration (later) | — | server + gateway end-to-end against a stub provider | — |

LLM-dependent logic is tested against a **fake gateway client**, never a live
provider — fast, deterministic, no credentials. Live-provider tests, when added,
are opt-in behind an env flag and a build/marker, mirroring the layering in
`context/`.

## Conventions

- `gofmt` is law: run `make fmt` before committing (CI checks formatting).
- Keep handlers thin — business logic lives in `internal/` packages, not in
  `internal/handler`. See `context/backend-server.md`.
- New language → new `internal/lang/<code>/` package + one `Register` call.
- New task type → one file implementing `tasks.TaskType` + one `Register` call.
- The OpenAPI spec in `spec/` is the contract; update it in the same change that
  adds or alters an endpoint.

## Where things live

- **Design / current plan / scratch notes** → `context/` (may be stale; one topic
  per file). The rewrite plan and status live in `context/rewrite-status.md`.
- **Golden-state, verified documentation** → `docs/`. Promote notes from
  `context/` to `docs/` once they stop changing and are true.
