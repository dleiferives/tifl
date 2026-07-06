# Hosted Database Design

Status: working design note
Date: 2026-07-06

This note refines the database direction for a hosted tifl deployment. The
question is not "can Postgres handle this?" in the abstract. The question is how
learner traffic actually hits the database, and how we shape the schema so the
common flows stay cheap at scale.

The target remains Postgres as the primary source of truth, with supporting
systems only when a real bottleneck appears.

## Core Thesis

Most tifl data is per-user and not CDN-cacheable:

- Generated stories are usually unique.
- Tasks are generated per session.
- Learner state is per user.
- Reader events are per user/session/content.

So the database has to be designed around the app's hot paths.

The right model is:

```text
Postgres stores truth.
Artifacts make screens fast.
Events preserve history.
Workers turn events into current state.
Summaries make list pages cheap.
```

This is more complex than the current normalized design, but it moves work away
from hot request paths and into explicit, rebuildable read models.

## 1k Requests Per Second: Real Flow

At 1k requests/second, the problem is not that Go cannot serve requests. The
problem is database pressure, connection count, write amplification, and LLM/job
latency.

A realistic healthy traffic mix might look like:

```text
250/s home/library/bootstrap reads
200/s reader bundle reads
250/s event-batch writes
100/s task bundle or task submit
50/s generation commands/progress checks
100/s lookup/definition requests
50/s settings/auth/misc
```

This is manageable if each endpoint is bounded and cheap.

The unhealthy version is:

```text
reader load = many resource calls
reader render = hundreds of token rows + broad learner-state scan
word rating = one request/write per keypress
task page = repeated joins
generation = long request with LLM calls inside it
```

The healthy version is:

```text
reader load = one route bundle
reader render = one artifact + relevant learner overlay
word rating = local update + batched/coalesced write
task submit = one transaction
generation = queued job
```

## Table Roles

Every table should have one clear role:

- Truth: canonical state the product cannot lose.
- Read model: derived data optimized for a screen/query.
- Log: append-only history or signals.
- Cache: reusable computed data that can be regenerated.

If a table's role is unclear, the schema is probably muddy.

## Target Data Model

### Global Catalogue And Cache

Shared across users:

```text
languages
knowledge_items
skills
item_skill_associations
global_definitions
global_breakdowns
sentence_structures
cached_phrases
```

This data is read-heavy, slow-changing, and cacheable in API memory or Redis
later if needed. It should stay relational because the app needs lookups and
joins by language/item/skill.

### Users And Auth

Small, normal relational tables:

```text
users
refresh_tokens
auth_security_events
user_profiles
user_settings
```

These are not the scaling problem. Keep them boring.

### Learner State

Current derived state used by selection, reader rendering, and review:

```text
learner_items
  user_id
  item_id
  level
  confidence_score
  exposure_count
  lookup_count
  task_correct
  task_total
  fsrs_difficulty
  fsrs_stability
  fsrs_last_review
  updated_at

learner_surface_forms
  user_id
  language
  item_key
  surface_key
  level
  updated_at

learner_skills
  user_id
  skill_id
  xp
  tier
  pending_verify
  updated_at
```

These tables are the current state. They are updated by task grading and event
workers. They should be compact and indexed by `user_id`.

Important reader rule:

```text
Do not load all learner state for a language on every reader open.
Load only learner rows relevant to the current content.
```

### Content

Use a general content model rather than treating stories as the only renderable
unit.

```text
content
  content_id
  user_id
  type              -- story, imported_text, phrase_set, dialogue, explanation
  language
  title
  source_kind       -- generated, imported, explanation, dialogue, etc.
  source_text
  level
  created_at
  updated_at

content_artifacts
  content_id
  revision
  render_json       -- token stream / turns / phrase rows / mixed segments
  glossary_json
  metadata_json
  created_at

content_items
  content_id
  item_id
  item_key
  surface_key
  first_position
  occurrence_count
  role              -- target, new, background, incidental
```

`content_artifacts` are the fast render path. The app usually reads a whole
story, dialogue, explanation, or phrase set at once.

`content_items` keeps item-level queryability. It supports:

- Loading only relevant learner state.
- Task target linkage.
- Coverage analysis.
- Review receipts.
- Selector/eval analysis.

This is the main redesign from the current `story_tokens`-as-primary-render-path
model.

### Long Content Sections

Long imports should not become one enormous artifact.

```text
content_sections
  section_id
  content_id
  section_index
  title
  source_text
  render_json
  revision
```

Generated short stories can have one section. EPUB/PDF imports can be split by
chapter/section/article.

### Sessions

Sessions represent a learner workflow around content.

```text
sessions
  session_id
  user_id
  content_id
  content_type
  language
  level
  status
  created_at
  reading_started_at
  completed_at
  archived_at
```

This makes generated stories, imported texts, phrase sets, dialogue sessions,
and explanation sessions fit one shell model.

### Session Summaries

Home and Library should not join the world.

```text
session_summaries
  session_id
  user_id
  content_id
  title
  content_type
  status
  language
  level
  task_count
  graded_task_count
  target_count
  last_activity_at
  archived_at
```

Hot query:

```sql
SELECT *
FROM session_summaries
WHERE user_id = $1
  AND archived_at IS NULL
ORDER BY last_activity_at DESC, session_id DESC
LIMIT 30;
```

This should be one cheap indexed query.

### Tasks And Attempts

Tasks should separate immutable prompt content from learner attempts.

```text
tasks
  task_id
  session_id
  user_id
  task_type
  language
  content_json
  answer_key_json
  status
  latest_attempt_id
  created_at

task_targets
  task_id
  item_id

task_attempts
  attempt_id
  task_id
  user_id
  response_json
  grade_json
  graded_by
  created_at
```

Benefits:

- Review can show current/latest attempt.
- Evals can inspect all attempts.
- Regeneration/reporting can preserve the rejected task and lineage.
- Knowledge updates can be tied to specific attempts.

### Client Events

High-volume behavior goes into an append-only event table.

```text
client_events
  event_id
  user_id
  session_id
  content_id
  event_type
  item_id
  position
  payload_json
  occurred_at
  processed_at
```

Partition by time, probably monthly.

Do not log every cursor movement or every token exposure as a row. Prefer
compressed/high-value events:

```text
read_started
read_completed
read_span_summary
lookup
rating_change
peek
confusion
preview_guess
```

Example compressed event:

```json
{
  "event_type": "read_span_summary",
  "content_id": "cnt_123",
  "from_position": 0,
  "to_position": 180,
  "duration_ms": 90000
}
```

Workers fold events into:

```text
learner_items
learner_surface_forms
session_summaries
```

### Jobs

Background work should be explicit:

```text
jobs
  job_id
  user_id
  kind
  status
  payload_json
  attempts
  run_after
  leased_by
  leased_until
  created_at
  updated_at
```

Used for:

- Generation.
- Reader event processing.
- Task regeneration.
- Skill verification.
- Import extraction.
- TTS.

Jobs must be idempotent: a retry should not corrupt state.

### LLM Calls

Split metadata from large debug payloads.

```text
llm_calls
  call_id
  user_id
  session_id
  job_id
  kind
  prompt_version
  model
  input_tokens
  output_tokens
  latency_ms
  status
  called_at

llm_call_payloads
  call_id
  system_prompt
  user_prompt
  raw_response
  parsed_output
  error_payload
```

Normal debug lists should load `llm_calls`. Large payloads load only when a row
is expanded.

## Hot User Flows

### Open App

```text
GET /bootstrap
```

Reads:

- user/profile
- enabled languages
- feature flags
- minimal app settings

Should not load sessions, story content, task JSON, or debug data.

### Home

```text
GET /home
```

Reads:

- `session_summaries`
- maybe a small active generation/job summary

Avoid:

- task joins
- story token/artifact loads
- LLM payloads

### Library

```text
GET /library?cursor=...
```

Reads:

- generated/imported content summaries
- session summaries where relevant

Use cursor pagination. Avoid deep `OFFSET` at scale.

### Open Reader

```text
GET /sessions/{id}/bundle?view=read
```

Reads:

- session authorization/state
- content artifact
- content item index
- learner items for only those item IDs
- learner surface levels for only those surface keys

Bounded by current content, not by the user's entire language history.

### Reading

Client:

- updates UI locally
- queues events/outbox rows
- coalesces ratings

Server:

```text
POST /client-events/batch
```

Writes:

- multi-row insert into `client_events`
- enqueue processing job when needed

### Task Page

```text
GET /sessions/{id}/bundle?view=tasks
```

Reads:

- tasks for one session
- latest attempt state
- maybe session progress summary

### Submit Task

One transaction:

```text
insert task_attempt
update task latest_attempt/status
update learner_items for target items
insert XP logs
update session_summary
```

Do not call LLM inside the DB transaction. If grading is LLM-backed and slow, use
a pending grade/job path.

### Generate Session

HTTP request:

```text
insert session
insert job
return 202
```

Worker:

```text
claim job
call LLM
write content
write content_artifact
write content_items
write tasks
write session_summary
mark job done
```

### Debug

Reads:

- `llm_calls` metadata
- job/stage status
- payload rows only on demand

Debug data should not be part of normal app bundles.

## Indexing Strategy

Indexes must match real queries. They are not free: each write updates every
relevant index.

Likely important indexes:

```sql
-- Home/library.
CREATE INDEX idx_session_summaries_user_active
  ON session_summaries(user_id, last_activity_at DESC, session_id DESC)
  WHERE archived_at IS NULL;

CREATE INDEX idx_session_summaries_user_archived
  ON session_summaries(user_id, archived_at DESC, session_id DESC)
  WHERE archived_at IS NOT NULL;

-- Reader bundle.
CREATE INDEX idx_content_items_content
  ON content_items(content_id);

CREATE INDEX idx_learner_items_user_item
  ON learner_items(user_id, item_id);

CREATE INDEX idx_learner_surface_user_lang_key
  ON learner_surface_forms(user_id, language, item_key, surface_key);

-- Tasks.
CREATE INDEX idx_tasks_session
  ON tasks(session_id, created_at, task_id);

CREATE INDEX idx_task_attempts_task_created
  ON task_attempts(task_id, created_at DESC);

-- Events.
CREATE INDEX idx_client_events_unprocessed
  ON client_events(user_id, content_id, occurred_at)
  WHERE processed_at IS NULL;

-- LLM budgets/debug.
CREATE INDEX idx_llm_calls_user_date
  ON llm_calls(user_id, called_at);

CREATE INDEX idx_llm_calls_session_date
  ON llm_calls(session_id, called_at);
```

Consider BRIN indexes for huge append-only time-ordered tables such as
`client_events` and `llm_calls`.

Use JSONB indexes only when the app has a real query inside a JSON field. Do not
GIN-index every JSONB artifact by default.

## Connection Pooling

At 1k requests/second, connection count is a real limit.

Bad:

```text
50 API instances * 50 DB connections = 2500 DB connections
```

Good:

```text
API instances
  -> small local pool
  -> PgBouncer/provider pooler
  -> Postgres
```

Keep transactions short. Avoid network calls inside transactions. Avoid
session-bound Postgres behavior if using transaction pooling.

## Why Postgres Still Wins

Postgres remains the best primary database because tifl needs:

- transactions
- relational ownership and joins
- user-scoped state
- append-only logs
- JSONB artifacts
- partial indexes
- range partitioning
- mature connection pooling

Other databases are useful references, not replacements:

- DynamoDB is excellent for fixed key-value access patterns, but tifl's learning
  state and evolving relational queries would make the primary schema harder.
- Firestore has mobile/offline strengths, but the document model and read/write
  cost model are not ideal for this relational learning system.
- MongoDB supports embedded documents well, but Postgres JSONB gives enough
  artifact storage while keeping SQL.
- CockroachDB may matter later for multi-region writes, but it adds distributed
  SQL complexity before we need it.

## Dependency Discipline

Start with:

```text
Postgres
Go API
Go worker
Cloud static hosting/CDN
LLM gateway/provider
```

Add only when pressure proves it:

```text
PgBouncer/provider pooler: likely needed at scale
Redis/Valkey: shared cache/rate/progress state if needed
SQS/Cloud Tasks: if Postgres-backed jobs become pressure
Object storage: audio, PDFs, EPUBs, huge artifacts
Analytics/archive storage: old events/logs
```

Do not add Kafka, DynamoDB, Elasticsearch, or a warehouse at the start.

## Complexity Compared With Current System

The current system is simpler:

```text
normalized tables
resource endpoints
direct reads/writes
story_tokens as rows
reader_events as rows
tasks store latest response/grade
```

The proposed system adds:

```text
content_artifacts
content_items
session_summaries
task_attempts
client_events
derived-state workers
```

This is more schema/design complexity, but better runtime behavior.

| Area | Current | Proposed |
| --- | --- | --- |
| Schema | simpler | more tables, clearer roles |
| Reader load | many rows / broader state | one artifact + bounded overlay |
| Home/library | computed from live tables | summary table |
| Events | direct append + processing | batched append + worker processing |
| Tasks | latest response on task | immutable task + attempts |
| Scaling | easier to start | easier to scale |
| Bugs | fewer moving parts | derived-data drift possible |
| Performance | okay early | much better at high traffic |

The real added risks:

- Derived rows can drift.
- Workers become more important.
- Rebuild paths are required.
- Table ownership must stay clear.

Mitigation:

- Every derived table gets a rebuild command.
- Every worker is idempotent.
- Every bundle/read model has a revision.
- Every hot query gets measured with `EXPLAIN ANALYZE`.

## Phased Approach

Do not rewrite the whole schema at once.

1. Add `session_summaries`.
2. Add `content_artifacts` while keeping existing token rows.
3. Add `content_items` and reader overlay loading by current content.
4. Add route bundles that use summaries/artifacts.
5. Add batched `client_events`.
6. Add `task_attempts`.
7. Split `llm_call_payloads`.
8. Partition event/log tables once volume justifies it.

This gives performance gains without a risky big-bang migration.

## Research Anchors

- PostgreSQL partial indexes: https://www.postgresql.org/docs/current/indexes-partial.html
- PostgreSQL partitioning and pruning: https://www.postgresql.org/docs/current/ddl-partitioning.html
- PostgreSQL EXPLAIN/query planning: https://www.postgresql.org/docs/current/using-explain.html
- PostgreSQL connection settings: https://www.postgresql.org/docs/current/runtime-config-connection.html
- PgBouncer pooling: https://www.pgbouncer.org/config.html
- PostgreSQL JSONB: https://www.postgresql.org/docs/current/datatype-json.html
- PostgreSQL TOAST: https://www.postgresql.org/docs/current/storage-toast.html
- PostgreSQL BRIN indexes: https://www.postgresql.org/docs/current/brin.html
- PostgreSQL bulk insert guidance: https://www.postgresql.org/docs/current/populate.html
- Cloud Run concurrency and max instances:
  https://docs.cloud.google.com/run/docs/configuring/concurrency
  and https://docs.cloud.google.com/run/docs/configuring/max-instances
- Amazon SQS visibility timeout and at-least-once delivery:
  https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-visibility-timeout.html
  and https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/standard-queues-at-least-once-delivery.html
