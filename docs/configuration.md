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
- **One-off listen address:** `-addr HOST:PORT` on either binary overrides the
  resolved `addr` for that process. Use port `0` to bind a random unused local
  port, e.g. `go run ./cmd/server -addr 127.0.0.1:0`; the startup log prints
  the actual selected URL.
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
  audio_base_url: ""
  audio_api_key: ""
  audio_tts_model: auto
  audio_tts_voice: auto
  audio_tts_speed: 0.9
  audio_stt_model: auto
  auth_mode: none
  jwt_secret: ""
  allow_insecure_auth_cookie: false
  frontend_dir: web/dist
  media_storage_mode: local
  media_local_root: data/media
  media_public_base_url: ""

gateway:  # cmd/gateway
  addr: 127.0.0.1:8001
  provider: opencode
  upstream_url: http://127.0.0.1:4202
  api_key: ""
  api_keys: []
  model: opencode/nemotron-3-ultra-free
  agent: writer
  balance: least_in_flight
  max_retries: 3
  base_delay_ms: 250
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
file counts as "unset" and falls through to the default. For listen addresses
only, the `-addr` command-line flag is a final one-off override.

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
| `audio_base_url` | `AUDIO_BASE_URL` | — | OpenAI-compatible audio-server base URL; blank disables conversation TTS/STT |
| `audio_api_key` | `AUDIO_API_KEY` | — | optional audio-server bearer token |
| `audio_tts_model` | `AUDIO_TTS_MODEL` | `auto` | model sent to `/v1/audio/speech` |
| `audio_tts_voice` | `AUDIO_TTS_VOICE` | `auto` | voice sent to `/v1/audio/speech` |
| `audio_tts_speed` | `AUDIO_TTS_SPEED` | `0.9` | speech speed multiplier (`0.25`–`4`) |
| `audio_stt_model` | `AUDIO_STT_MODEL` | `auto` | model sent to `/v1/audio/transcriptions` |
| `auth_mode` | `AUTH_MODE` | `none` | `none` (synthetic local user) or `jwt` (cloud) |
| `jwt_secret` | `JWT_SECRET` | — | JWT signing key (required when `auth_mode: jwt`) |
| `allow_insecure_auth_cookie` | `ALLOW_INSECURE_AUTH_COOKIE` | `false` | development-only: allow the refresh cookie over HTTP |
| `frontend_dir` | `FRONTEND_DIR` | `web/dist` | compiled SolidJS assets to serve |
| `media_storage_mode` | `MEDIA_STORAGE_MODE` | `local` | binary media storage mode: `local` or S3-compatible |
| `media_local_root` | `MEDIA_LOCAL_ROOT` | `data/media` | local object root for audio/scans/uploads |
| `media_public_base_url` | `MEDIA_PUBLIC_BASE_URL` | — | URL prefix for served local media or public S3/CDN objects |
| `media_s3_bucket` | `MEDIA_S3_BUCKET` | — | S3-compatible bucket |
| `media_s3_endpoint` | `MEDIA_S3_ENDPOINT` | — | optional S3-compatible endpoint, e.g. R2/MinIO |
| `media_s3_region` | `MEDIA_S3_REGION` | — | S3 region; R2 commonly uses `auto` |
| `media_s3_access_key_env` | `MEDIA_S3_ACCESS_KEY_ENV` | `AWS_ACCESS_KEY_ID` | env var name for S3 access key |
| `media_s3_secret_key_env` | `MEDIA_S3_SECRET_KEY_ENV` | `AWS_SECRET_ACCESS_KEY` | env var name for S3 secret key |
| `media_s3_signed_urls` | `MEDIA_S3_SIGNED_URLS` | `true` | presign S3 media URLs by default |

The server never calls a model provider directly — only the gateway at
`llm_base_url`. See [backend-server](../context/backend-server.md).

Conversation audio is also server-proxied: browsers use authenticated Tifl
routes while the API server calls the service at `audio_base_url`. This keeps
machine-local hostnames and audio credentials out of the web client. The checked-in
VS Code launch profile supplies the development-only `http://prometheus:8010`
value; deployments should set their own URL or leave audio disabled.

Media object rows should store stable object keys such as
`story_audio/{story_id}/{audio_id}.mp3`, not absolute filesystem paths or
provider URLs. The local store maps those keys under `media_local_root`; S3 mode
maps the same keys into a bucket. Task media access goes through authenticated
task-scoped routes such as `/api/v1/tasks/{id}/media/url`, not arbitrary object
key lookups.

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
| `api_keys` | `GATEWAY_API_KEYS` | — | comma-separated/list credentials; each key becomes a balanced endpoint |
| `model` | `GATEWAY_MODEL` | `openrouter/free` for OpenRouter; otherwise — | default model when a request omits one |
| `agent` | `GATEWAY_AGENT` | `writer` | OpenCode only: agent to drive |
| `balance` | `GATEWAY_BALANCE` | `least_in_flight` | `least_in_flight` or `round_robin` across configured endpoints |
| `max_retries` | `GATEWAY_MAX_RETRIES` | `3` | transient upstream retries |
| `base_delay_ms` | `GATEWAY_BASE_DELAY_MS` | `250` | first retry backoff delay |

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

For a same-provider key pool in hosted deployments, use:

```bash
GATEWAY_PROVIDER=openrouter \
GATEWAY_API_KEYS=sk-1,sk-2,sk-3 \
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

### Multiple keys and gateways

Use `api_keys` when one provider account has multiple credentials to spread
rate-limit pressure. The gateway builds one selectable endpoint per key:

```yaml
gateway:
  provider: openrouter
  api_keys:
    - sk-or-1
    - sk-or-2
  model: openrouter/free
```

Use `gateways` when routing across multiple upstreams/providers:

```yaml
gateway:
  balance: least_in_flight
  max_retries: 3
  base_delay_ms: 250

  gateways:
    - name: openrouter-main
      provider: openrouter
      api_keys:
        - sk-or-1
        - sk-or-2
      model: openrouter/free
      models:
        - openrouter/free

    - name: anthropic-direct
      provider: anthropic
      api_keys:
        - sk-ant-1
      model: claude-3-5-haiku-latest
      models:
        - claude-3-5-haiku-latest
```

When `gateways` is present, each entry may set `name`, `provider`,
`upstream_url`, `api_key`, `api_keys`, `model`, `agent`, and `models`. `name` is
used only for logs. `models` is an optional exact allow-list; if omitted, that
endpoint may receive any requested model.

`least_in_flight` is the default because LLM calls have uneven latency. The
gateway also respects upstream `Retry-After` headers, cools down rate-limited or
overloaded endpoints, and disables endpoints that return credential/billing
errors. Logs identify the selected gateway and key label but never print raw
API keys.

This is load balancing, not model fallback. The gateway does not yet rewrite a
requested model to a different fallback model after model-unavailable,
context-length, or capability errors.

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
