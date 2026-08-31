# System Architecture Overview

_Status: active design notes — last updated during initial architecture session_

## What This System Is

A language acquisition platform built around comprehensible input. The primary
workflow lets the user add stories in a target language and optionally generate
tasks derived from them. The system uses every interaction to build a model of
what they know. When the user wants new material, Tifl can generate a targeted
story from that model. The loop tightens over time: better knowledge of the user
→ better-targeted practice and generated content → faster acquisition.

The pedagogical foundation is not memorization. It is acquisition through
massive, contextually rich, level-appropriate exposure — the learner encounters
vocabulary and constructions so many times across so many contexts that they
become second nature, with no translation layer. Phrase-based learning is central:
the unit of acquisition is not a word but a form-meaning pair at any granularity
(word, phrase, construction, idiom).

---

## The Three-Layer Stack

```
┌─────────────────────────────────────────────┐
│  Client                                     │
│  SolidJS web app (TypeScript → JS)          │
│  Runs in: browser / Tauri webview /         │
│           Capacitor webview                 │
└──────────────────┬──────────────────────────┘
                   │ HTTP  /api/v1/...
┌──────────────────▼──────────────────────────┐
│  API Server  (Go)                           │
│  Serves HTML + JSON API                     │
│  Owns: business logic, DB, auth,            │
│        selection layer, task system         │
└──────────────────┬──────────────────────────┘
                   │ OpenAI-compatible HTTP
┌──────────────────▼──────────────────────────┐
│  LLM Gateway  (Go, separate process)        │
│  Speaks OpenAI API inbound                  │
│  Routes to: OpenRouter / Anthropic /        │
│             Ollama / any future provider    │
└─────────────────────────────────────────────┘
```

The API server never talks directly to a model provider. It talks to the gateway.
Swapping providers is a gateway config change; the application server is unaffected.

---

## Monorepo Layout

```
tifl/
├── cmd/
│   ├── server/        Go API server entrypoint
│   └── gateway/       LLM gateway entrypoint
├── internal/
│   ├── db/            repository interface + SQLite/Postgres implementations
│   ├── lang/          language plugin system (greek/, arabic/, chinese/, ...)
│   ├── llm/           gateway client, provider adapters, prompt builders
│   ├── reader/        reader domain: tokenization, knowledge lookup
│   ├── story/         story generation pipeline
│   ├── tasks/         task plugin registry and task execution
│   ├── selector/      selection layer: item prioritization for prompts
│   ├── predictor/     knowledge predictor (algorithmic + ML)
│   ├── auth/          JWT, password hashing, session management
│   └── handler/       HTTP handlers (HTML routes + JSON API routes)
├── web/               SolidJS frontend (TypeScript, compiled by esbuild)
├── desktop/           Tauri shell (src-tauri/, bundles Go binary + web/)
├── mobile/            Capacitor shell (wraps web/, targets cloud API)
└── spec/              OpenAPI spec — canonical API contract
```

---

## Core Design Principles

### 1. The API is the only contract

Every client — browser, desktop app, mobile app — is an HTTP client. No client
has privileged access or shared memory with the server. The OpenAPI spec in
`spec/` is the contract. Clients are generated or hand-written against it; they
do not reach into server internals.

### 2. Hard system + soft system

Two kinds of intelligence cooperate throughout:

- **Hard system**: counting, storing, threshold computation, item selection,
  scheduling. Fast, deterministic, cheap, runs entirely in Go with no external
  calls. This is what runs on every request.

- **Soft system**: the LLM. Generates stories, generates tasks, grades open-ended
  responses, assesses acquisition edge cases. Slow, expensive, non-deterministic.
  Called only when genuinely needed.

The selection layer is the boundary. The hard system decides *what* to put in a
prompt. The soft system decides *what to do* with it. The LLM should never be
asked to do work the hard system can do.

### 3. Language as a plugin

Every language-specific concern — tokenization, lemmatization, what a "knowledge
item" is, what task types make sense — lives in a language plugin. The core system
is language-agnostic. Adding a new language means adding a new plugin directory
and registering it; nothing in core changes. See: `context/language-plugins.md`.

### 4. Extensibility by interface, not inheritance

Task types, knowledge predictors, LLM providers, storage backends — all are
interfaces with multiple implementations. New task types, new providers, and new
storage backends can be added without touching existing code. This is the primary
mechanism for keeping the system composable as it grows.

### 5. Log everything, train later

The system's ML predictor (for knowledge probability estimation) does not exist
yet. What does exist is the data it will need: every reader interaction, every
task attempt, every lookup. This data is logged from day one. When enough users
have accumulated enough sessions, the predictor can be trained on it. The logging
schema is designed for this; it is not an afterthought.

---

## Subsystem Map

| Subsystem | What it does | Doc |
|-----------|-------------|-----|
| Language plugins | Tokenization, lemmatization, item types per language | `language-plugins.md` |
| Knowledge & acquisition | Tracking what the user knows and how well | `knowledge-acquisition.md` |
| Skill system | XP-based skill tiers, level promotion, story complexity control | `skill-system.md` |
| Session types | System-driven, topic-guided, expression-guided sessions | `session-types.md` |
| Selection layer | Choosing what to put in each LLM prompt | `selection-layer.md` |
| Task system | Modular, extensible task types and grading | `task-system.md` |
| Prompting system | Composable prompt builders over shared context | `prompting-system.md` |
| Reader mode | The interactive reading UI | `reader-mode.md` |
| Knowledge predictor | Estimating knowledge probability | `knowledge-predictor.md` |
| Database schema | All tables, annotated | `database-schema.md` |
| Auth & users | Auth, multi-tenancy, local vs cloud | `auth-users.md` |
| Input modalities | Speech, audio, print/scan | `input-modalities.md` |
| Frontend architecture | SolidJS, signals, TypeScript, esbuild | `frontend-architecture.md` |
| Backend server | Go server, API surfaces, repo pattern | `backend-server.md` |
| Deployment & platforms | Web, Tauri desktop, Capacitor mobile | `deployment-platforms.md` |

---

## The Learning Loop (end to end)

```
1. User profile established
   └─ language, current level, existing knowledge_items

2. Selection layer runs (pure Go, no LLM)
   └─ queries user_knowledge, runs predictor
   └─ produces: targets (5-10), background (30-40), new (3-5)

3. Story generator prompt built from SelectedItems + LearnerCtx
   └─ LLM produces story text
   └─ story tokenized server-side → story_tokens table

4. Task generator produces tasks for the story
   └─ task types chosen by language plugin + user level
   └─ tasks stored with JSON content blobs

5. User reads story in reader mode
   └─ lookup_count increments on every Space keypress
   └─ knowledge level ratings (1-5/w/i) saved optimistically

6. User completes tasks
   └─ responses stored
   └─ grader runs (rule-based or LLM depending on task type)
   └─ grade stored in task.grade JSON

7. Signal aggregation
   └─ exposure_count, lookup_count, task_correct/total updated
   └─ acquisition_stage transitions computed
   └─ knowledge_predictions invalidated → recomputed in background

8. Go to step 2 for next session
   └─ richer knowledge state → better-targeted content
```

---

## Deployment Modes

| Mode | Storage | Auth | LLM routing |
|------|---------|------|-------------|
| Cloud/web | Postgres | JWT | Through your gateway → OpenRouter |
| Desktop local | SQLite (local file) | None (synthetic user) | Through your gateway or direct |
| Desktop synced | SQLite + sync | JWT | Through your gateway |
| Mobile | Remote Postgres | JWT | Through your gateway |

Same Go binary, different config. See `deployment-platforms.md`.

---

## Open Questions

- Exact SRS algorithm for internal item scheduling within the selector
- Phrase/construction discovery pipeline (how new constructions are identified in
  generated stories and surfaced as knowledge_items)
- Sync protocol for desktop-local → cloud migration
- ML predictor training pipeline infrastructure
