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
| Live gateway | `make test-live` | gateway → real provider end-to-end (client → gateway → OpenCode → model) | a running `opencode serve` |
| Integration (later) | — | server + gateway end-to-end against a stub provider | — |

LLM-dependent logic is tested against a **fake gateway client**, never a live
provider — fast, deterministic, no credentials. The one live path is opt-in:
`make test-live` is gated behind the `live` build tag **and** the
`TIFL_LIVE_OPENCODE_URL` env var, so `make test` never touches the network. It
double-verifies the gateway (#3) against a local OpenCode server (#30):

```sh
opencode serve --port 4202 --hostname 127.0.0.1     # in one terminal
make test-live                                       # in another (TIFL_LIVE_MODEL optional)
```

## Conventions

- `gofmt` is law: run `make fmt` before committing (CI checks formatting).
- Keep handlers thin — business logic lives in `internal/` packages, not in
  `internal/handler`. See `context/backend-server.md`.
- New language → new `internal/lang/<code>/` package + one `Register` call.
- New task type → one file implementing `tasks.TaskType` + one `Register` call.
- **Keep the core language-agnostic.** Anything a specific language knows about
  itself — tokenization, key/lemma resolution, answer normalization (case
  folding, accent/diacritic handling, script quirks like Greek final sigma),
  which task types make sense — lives behind the `lang.Language` interface in
  `internal/lang/<code>/`, never hardcoded in `internal/{story,tasks,selector,…}`.
  If you reach for a language-specific rule in core code, that rule belongs on the
  plugin interface with a sensible default. We are building for *many* languages,
  not a privileged few.
- **Test the core against a fake `lang.Language`, not a real plugin.** Pipeline,
  selector and task tests use a trivial in-test plugin so they verify
  orchestration, not one language's morphology. Language-specific behaviour is
  tested inside that language's own package (e.g. `internal/lang/el`). A core test
  that imports `internal/lang/el` is a smell — it couples the engine to Greek.
- The OpenAPI spec in `spec/` is the contract; update it in the same change that
  adds or alters an endpoint.
- Endpoint changes must keep the client contract in lockstep: handler + tests,
  `spec/openapi.yaml`, regenerated web API types (`make web-api-types`), and a
  typed wrapper in `web/src/api.ts` belong in the same PR. `make web-typecheck`
  verifies that the checked-in generated types still match the spec.

## Where things live

- **Design / current plan / scratch notes** → `context/` (may be stale; one topic
  per file). The rewrite plan and status live in `context/rewrite-status.md`.
- **Golden-state, verified documentation** → `docs/`. Promote notes from
  `context/` to `docs/` once they stop changing and are true.
