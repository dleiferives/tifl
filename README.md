# tifl — Thinking In Foreign Languages

A language **acquisition** platform (not memorization). The learner reads
AI-generated stories at their level, does tasks derived from those stories, and
every interaction sharpens the system's model of what they know — which drives
the next round of generation. The pedagogy is comprehensible input and
phrase-based learning, not flashcards. See [`context/`](context/) for the full
design.

> **Status:** ground-up rewrite in progress. The Python proof-of-concept was
> replaced by this Go + SolidJS implementation. The pre-rewrite code is preserved
> at the git tag `pre-rewrite-snapshot` (branch `archive/python-prototype`).
> Current progress: [`context/rewrite-status.md`](context/rewrite-status.md).

## Architecture

Three layers, one contract (the OpenAPI spec):

```
SolidJS client  ──HTTP /api/v1──>  Go API server  ──OpenAI-compatible──>  Go LLM gateway  ──>  provider
(browser / Tauri / Capacitor)      (DB, auth, selection,                  (provider routing,
                                    tasks, pipeline)                       credentials, logging)
```

The API server never talks to a model provider directly — only to the gateway,
so swapping providers is a gateway config change. A **hard system** (counting,
selection, scheduling — fast, deterministic Go) and a **soft system** (the LLM —
generation, grading) cooperate, with the selection layer as the boundary.

See [`context/architecture-overview.md`](context/architecture-overview.md).

## Layout

```
cmd/
  server/      API server entrypoint (serves the client + /api/v1)
  gateway/     LLM gateway entrypoint (OpenAI-compatible proxy)
internal/
  config/      env-driven configuration
  domain/      core language-agnostic types (KnowledgeItem, SelectedItems, ...)
  db/          Repository interface + migrations/ (canonical schema)
  lang/        language plugin registry (greek/, arabic/, ... land here)
  llm/         gateway client + prompt-builder contract
  reader/      tokenization + reader signals
  story/       generation pipeline (staged, checkpointed)
  tasks/       extensible task-type registry + grading
  selector/    selection layer (hard-system boundary)
  predictor/   knowledge-probability estimation (algorithmic -> ML)
  auth/        JWT, argon2id, refresh tokens
  handler/     thin HTTP handlers
web/           SolidJS + TypeScript client (esbuild -> dist/)
spec/          OpenAPI spec — the canonical API contract
context/       design docs (the spec for this build) + working notes
docs/          golden-state docs (authoritative; becomes published docs)
```

## Run

```bash
make run            # API server on http://127.0.0.1:8000  (/healthz, /api/v1/ping)
make run-gateway    # LLM gateway on http://127.0.0.1:8001

make web-install    # one-time: install web deps
make web            # build the SolidJS client into web/dist (then `make run` serves it)
```

With no web build, the server still runs and serves a placeholder at `/`.

Both binaries read a single YAML config (`./tifl.yaml` by default,
`-config PATH` to override) — no env vars needed. Copy the example and edit:

```bash
cp tifl.config.example.yaml tifl.yaml
```

The example is wired for a credential-free local model via a running
[OpenCode](https://opencode.ai) server — the `gateway.provider: opencode` path:

```bash
opencode serve --port 4202 --hostname 127.0.0.1   # one terminal
make run-gateway                                   # reads tifl.yaml; no env vars
make run
```

Every key can still be overridden by its env var (e.g. `GATEWAY_MODEL`) for CI or
one-off runs — precedence is defaults < `tifl.yaml` < environment. Providers:
`ollama` (default), `openrouter`, `openai`, `anthropic`, `opencode`.

## Develop

```bash
make build   # both Go binaries -> bin/
make test    # go test ./...
make vet     # go vet ./...
make fmt     # gofmt -w
```

Contributing & test layers: [CONTRIBUTING.md](CONTRIBUTING.md).

## Configuration

The same binary runs cloud (Postgres/JWT) and desktop-local (SQLite/no-auth)
from a single YAML file (`tifl.yaml`), with env-var overrides. Full reference,
including every key and the gateway providers: **[docs/configuration.md](docs/configuration.md)**.
Quick map of the common API-server keys (`server:` section / env override):

| `tifl.yaml` (`server:`) | Env override | Default | Effect |
|-------------------------|--------------|---------|--------|
| `addr` | `TIFL_ADDR` | `127.0.0.1:8000` | API server listen address |
| `storage_mode` | `STORAGE_MODE` | `sqlite` | `sqlite` or `postgres` |
| `db_path` | `DB_PATH` | `data/tifl.db` | SQLite file (sqlite mode) |
| `database_url` | `DATABASE_URL` | — | Postgres DSN (postgres mode) |
| `llm_base_url` | `LLM_BASE_URL` | `http://127.0.0.1:8001` | where the gateway listens |
| `auth_mode` | `AUTH_MODE` | `none` | `jwt` (cloud) or `none` (desktop-local) |
| `frontend_dir` | `FRONTEND_DIR` | `web/dist` | compiled client assets |
