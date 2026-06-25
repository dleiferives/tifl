# Configuration

Both tifl binaries — the API server (`cmd/server`) and the LLM gateway
(`cmd/gateway`) — are configured by a single **YAML file** plus optional
environment-variable overrides. No env vars are required: copy the example,
edit it, and run.

```bash
cp tifl.config.example.yaml tifl.yaml
make run-gateway     # reads ./tifl.yaml
make run             # reads ./tifl.yaml
```

The loader lives in [`internal/config`](../internal/config/config.go); the
annotated template is [`tifl.config.example.yaml`](../tifl.config.example.yaml).

## The config file

- **Default path:** `./tifl.yaml` (current working directory).
- **Override path:** `-config PATH` on either binary
  (`go run ./cmd/gateway -config /etc/tifl/prod.yaml`).
- **Optional:** if the file does not exist, built-in defaults apply and the
  binary still starts. A file that exists but fails to parse is a fatal error
  (so a typo is loud, never silently ignored).
- **Local-only:** `tifl.yaml` is git-ignored. Commit changes to
  `tifl.config.example.yaml` instead.

The file has two top-level sections; each binary reads only the one it needs:

```yaml
server:   # cmd/server
  addr: 127.0.0.1:8000
  storage_mode: sqlite
  db_path: data/tifl.db
  database_url: ""
  llm_base_url: http://127.0.0.1:8001
  llm_api_key: ""
  llm_model: ""
  auth_mode: none
  jwt_secret: ""
  allow_insecure_auth_cookie: false
  frontend_dir: web/dist

gateway:  # cmd/gateway
  addr: 127.0.0.1:8001
  provider: opencode
  upstream_url: http://127.0.0.1:4202
  api_key: ""
  model: opencode/nemotron-3-ultra-free
  agent: writer
```

Every key is optional; an omitted key falls back to its default (below).

## Precedence

For each setting the resolved value is, in increasing priority:

```
built-in default  <  tifl.yaml value  <  environment variable
```

So `tifl.yaml` is the normal way to configure a deployment, and an env var still
wins for a one-off run or in CI — e.g. `GATEWAY_MODEL=… make run-gateway`
overrides the file's `gateway.model` for that run only. An empty string in the
file counts as "unset" and falls through to the default.

## `server:` keys

| Key | Env override | Default | Effect |
|-----|--------------|---------|--------|
| `addr` | `TIFL_ADDR` | `127.0.0.1:8000` | API server listen address |
| `storage_mode` | `STORAGE_MODE` | `sqlite` | `sqlite` (desktop-local) or `postgres` (cloud) |
| `db_path` | `DB_PATH` | `data/tifl.db` | SQLite file path (sqlite mode) |
| `database_url` | `DATABASE_URL` | — | Postgres DSN (postgres mode) |
| `llm_base_url` | `LLM_BASE_URL` | `http://127.0.0.1:8001` | where the gateway listens |
| `llm_api_key` | `LLM_API_KEY` | — | optional gateway auth token |
| `llm_model` | `LLM_MODEL` | — | model the server requests (blank ⇒ gateway default) |
| `auth_mode` | `AUTH_MODE` | `none` | `none` (synthetic local user) or `jwt` (cloud) |
| `jwt_secret` | `JWT_SECRET` | — | JWT signing key (required when `auth_mode: jwt`) |
| `allow_insecure_auth_cookie` | `ALLOW_INSECURE_AUTH_COOKIE` | `false` | development-only: allow the refresh cookie over HTTP |
| `frontend_dir` | `FRONTEND_DIR` | `web/dist` | compiled SolidJS assets to serve |

The server never calls a model provider directly — only the gateway at
`llm_base_url`. See [backend-server](../context/backend-server.md).

In JWT mode, `jwt_secret` must contain at least 32 bytes or startup fails.
Production deployments must leave `allow_insecure_auth_cookie` false and expose
the server over HTTPS. See [Authentication](authentication.md).

## `gateway:` keys

| Key | Env override | Default | Effect |
|-----|--------------|---------|--------|
| `addr` | `GATEWAY_ADDR` | `127.0.0.1:8001` | gateway listen address |
| `provider` | `GATEWAY_PROVIDER` | `ollama` | upstream: `ollama`, `openrouter`, `openai`, `anthropic`, `opencode` |
| `upstream_url` | `GATEWAY_UPSTREAM_URL` | per provider | upstream base URL (required for `opencode`) |
| `api_key` | `GATEWAY_API_KEY` | — | upstream credential |
| `model` | `GATEWAY_MODEL` | `openrouter/free` for OpenRouter; otherwise — | default model when a request omits one |
| `agent` | `GATEWAY_AGENT` | `writer` | OpenCode only: agent to drive |

`llm_base_url` (server) and `addr` (gateway) must point at each other — they
default to the same `127.0.0.1:8001` so the two processes connect out of the box.

### Secrets

Provider API keys belong on the gateway only. For local development, put the key
in your untracked `tifl.yaml`:

```yaml
gateway:
  provider: openrouter
  api_key: "<OPENROUTER_API_KEY>"
  model: openrouter/free
```

For CI or hosted deployments, prefer environment variables:

```bash
GATEWAY_PROVIDER=openrouter \
GATEWAY_API_KEY=... \
GATEWAY_MODEL=openrouter/free \
make run-gateway
```

The Go binaries do not load `.env` files automatically. `.env` is ignored so it
is safe as a local shell-helper file if your own tooling sources it, but the
first-class inputs are `tifl.yaml` and environment variables.

### Providers

| `provider` | `upstream_url` default | Notes |
|------------|------------------------|-------|
| `ollama` | `http://127.0.0.1:11434/v1` | local Ollama; no key |
| `openrouter` | `https://openrouter.ai/api/v1` | set `api_key` |
| `openai` | `https://api.openai.com/v1` | set `api_key` |
| `anthropic` | `https://api.anthropic.com` | set `api_key`; native Messages API mapped to OpenAI |
| `opencode` | — (required) | local [OpenCode](https://opencode.ai) server; credential-free |

When `provider: openrouter`, the gateway uses OpenRouter's OpenAI-compatible
base URL (`https://openrouter.ai/api/v1`), sends `api_key` as a bearer token, and
forwards chat requests to `/chat/completions`. The Settings screen can save a
per-user `llm_model` override; blank uses `gateway.model`. If `gateway.model` is
blank and `provider` is `openrouter`, the gateway default is `openrouter/free`.
The model field is an OpenRouter/OpenAI-compatible id such as `openai/gpt-4`, a
`:free` variant, or a latest alias like `~openai/gpt-latest`.

For `opencode`, `model` is a `providerID/modelID` pair (e.g.
`opencode/nemotron-3-ultra-free`) and the gateway drives the `agent` (the repo
ships a tool-less single-shot `writer` agent at `.opencode/agent/writer.md`). The
provider routing and mappings live in
[`internal/gateway`](../internal/gateway/); OpenCode specifics are in
[`opencode.go`](../internal/gateway/opencode.go).

## Example: local model, no credentials

```bash
# 1. a real model, free, local
opencode serve --port 4202 --hostname 127.0.0.1

# 2. tifl.yaml gateway section
#    provider: opencode
#    upstream_url: http://127.0.0.1:4202
#    model: opencode/nemotron-3-ultra-free

make run-gateway   # tifl gateway listening on http://127.0.0.1:8001 (provider=opencode)
make run           # API server on http://127.0.0.1:8000
```

The opt-in live test (`make test-live`) drives this same path end-to-end; see
[CONTRIBUTING.md](../CONTRIBUTING.md).
