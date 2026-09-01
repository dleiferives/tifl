# Database Schema

_Status: active design notes — schema is designed, not yet implemented in Go_

## Overview

Two storage backends share one schema definition: SQLite for local/desktop
deployments, PostgreSQL for the cloud SaaS. The repository interface abstracts
over both — handlers never know which backend is running. The schema is designed
with three priorities:

1. **Multi-tenancy from day one** — every user-owned row has `user_id`. No
   retrofitting auth later.
2. **JSON blobs for extensible type-specific data** — task content, task
   responses, knowledge item metadata. The schema never changes when a new task
   type or language is added; the application layer owns those schemas.
3. **Full signal logging** — every reader interaction, every task attempt, every
   lookup is stored. This is the training data for the future ML predictor.

---

## Table Reference

### `users`

Authentication and identity. One row per registered user.

```
users
  user_id       TEXT  PK          UUID, generated at registration
  email         TEXT  NOT NULL    unique, lowercased
  password_hash TEXT  NOT NULL    argon2id hash
  created_at    REAL  NOT NULL    Unix timestamp
  last_login    REAL              Unix timestamp, nullable
  settings      JSON              user preferences (theme, UI options)
```

`settings` is a catch-all for user preferences that don't need to be queried —
display themes, notification prefs, UI density. Queried rarely, stored cheaply.

### `refresh_tokens`

Server-side records for opaque refresh credentials. The raw 256-bit token exists
only in the client's httpOnly cookie; storage keeps its SHA-256 digest.

```
refresh_tokens
  token_hash       TEXT  PK
  family_id        TEXT  NOT NULL    one login/device rotation family
  user_id          TEXT  NOT NULL    FK → users.user_id
  issued_at        REAL  NOT NULL
  expires_at       REAL  NOT NULL
  revoked_at       REAL
  replaced_by_hash TEXT              next digest in the rotation family
```

Reuse of a row with `replaced_by_hash` revokes the active rows in that family.
Independent families allow multiple concurrent devices; logout-all revokes every
family for a user.

---

### `languages`

Registered language plugins. Populated at server startup from whatever language
plugins are compiled in. Serves as a foreign key target and a discovery endpoint.

```
languages
  code          TEXT  PK          BCP-47 style: "el", "ar", "zh", "tr"
  name          TEXT  NOT NULL    display name: "Greek", "Arabic"
  key_strategy  TEXT  NOT NULL    "surface" | "lemma" | "root" | "stem"
  enabled       BOOL  NOT NULL    false = plugin registered but not exposed to users
```

---

### `knowledge_items`

The unified vocabulary/phrase/construction table. Language-scoped, shared across
all users (the items themselves are not per-user; the user's relationship to them
is in `user_knowledge`).

```
knowledge_items
  item_id       TEXT  PK          generated ID
  language      TEXT  NOT NULL    FK → languages.code
  item_type     TEXT  NOT NULL    "word" | "phrase" | "construction" | "idiom"
                                  (language plugins may add their own subtypes)
  key           TEXT  NOT NULL    canonical lookup key per the language's key_strategy
                                  e.g. lemma "ἄνθρωπος", root "k-t-b", surface "的"
  frequency     INT               rank in language frequency list (1 = most common)
  metadata      JSON              type-specific; language-plugin-defined schema
                                  see below

  UNIQUE(language, item_type, key)
```

**metadata schemas by type** (examples; each language plugin defines its own):

```
word (Greek):
  { "gloss": "human being, person", "part_of_speech": "noun",
    "paradigm": "2nd decl. masculine", "example": "ὁ ἄνθρωπος τρέχει" }

construction (Greek):
  { "name": "genitive absolute", "pattern": "[noun/pronoun GEN] [participle GEN]",
    "meaning": "subordinate clause expressing circumstance independent of main clause",
    "example": "τοῦ βασιλέως ἀποθανόντος, οἱ πολῖται ἔφευγον",
    "gloss": "the king having died, the citizens fled" }

phrase (any language):
  { "gloss": "what are you doing?", "register": "informal",
    "example_context": "greeting between friends" }

root (Arabic):
  { "root": "k-t-b", "core_meaning": "writing/recording",
    "derived_forms": ["كَتَبَ (wrote)", "كاتِب (writer)", "مَكتَبة (library)"] }
```

---

### `user_knowledge`

The user's acquisition state for each knowledge item. This is the central table
for the learning model — the selection layer reads it on every generation request,
and it is updated after every reader session and every task completion.

```
user_knowledge
  user_id           TEXT  NOT NULL    FK → users.user_id
  item_id           TEXT  NOT NULL    FK → knowledge_items.item_id
  acquisition_stage TEXT  NOT NULL    "unseen" | "encountered" | "recognizing" |
                                      "acquiring" | "acquired" | "automatic"
  exposure_count    INT   NOT NULL    times seen in stories
  context_variety   INT   NOT NULL    distinct stories it appeared in
  lookup_count      INT   NOT NULL    times user pressed Space in reader (strong
                                      "not acquired" signal)
  task_correct      INT   NOT NULL    times correctly answered in tasks
  task_total        INT   NOT NULL    times tested in tasks
  last_seen         REAL              Unix timestamp
  last_targeted     REAL              last time the selector put this in targets[]
  confidence_score  REAL              0.0–1.0, computed by predictor
  next_target_after REAL              internal SRS-like scheduling for selector;
                                      not user-visible

  PRIMARY KEY (user_id, item_id)
```

**Key design notes:**

- `lookup_count` is the most valuable signal: if a user keeps looking up a word,
  they haven't acquired it regardless of `exposure_count`.
- `next_target_after` is purely internal scheduler state. It is how the selection
  layer avoids hammering the same item every session. Users never see it.
- `acquisition_stage` transitions are computed by the hard system (Go, no LLM)
  using threshold rules over the other columns. The LLM may intervene for edge
  cases (e.g. high exposure + low task performance = "recognizing but not
  understanding").

---

### `knowledge_predictions`

Cached output of the knowledge predictor. Recomputed in the background after each
session; the selection layer reads this rather than computing predictions on the
fly per request.

```
knowledge_predictions
  user_id             TEXT  NOT NULL    FK → users.user_id
  item_id             TEXT  NOT NULL    FK → knowledge_items.item_id
  predicted_prob      REAL  NOT NULL    0.0–1.0 probability user knows this item
  predictor_version   TEXT  NOT NULL    "algorithmic-v1" | "ml-v1" | etc.
  computed_at         REAL  NOT NULL    Unix timestamp

  PRIMARY KEY (user_id, item_id)
```

Stale predictions are not harmful — the selector falls back to `confidence_score`
from `user_knowledge` if a prediction is too old. This table is an optimisation,
not a requirement.

---

### `stories`

Story content, either generated or supplied by the user. Session-backed stories
belong to one learning session.

```
stories
  story_id          TEXT  PK
  user_id           TEXT  NOT NULL    FK → users.user_id
  language          TEXT  NOT NULL    FK → languages.code
  text              TEXT  NOT NULL    the full story prose
  level             TEXT  NOT NULL    difficulty level ID
  topic             TEXT              nullable, e.g. "going to the market"
  estimated_coverage REAL             fraction of tokens predicted-known at generation time
  generated_at      REAL  NOT NULL
  audio_id          TEXT              FK → story_audio.audio_id, null until audio generated
  session_id        TEXT              FK → sessions.session_id
```

---

### `story_tokens`

Server-side tokenization of each story. The reader does not tokenize client-side;
it receives this table's data and renders it. This is also what audio alignment
references.

```
story_tokens
  story_id    TEXT  NOT NULL    FK → stories.story_id
  position    INT   NOT NULL    0-indexed token position in the story
  surface     TEXT  NOT NULL    display form including punctuation: "Ἄνθρωπόν,"
  item_key    TEXT              normalized key for knowledge lookup: "ἄνθρωπος"
                                null for non-word tokens (spaces, punctuation)
  surface_key TEXT              language-owned exact-form key for reader ratings
  is_word     BOOL  NOT NULL    false for whitespace and punctuation-only tokens

  PRIMARY KEY (story_id, position)
```

A story token with `is_word = false` is rendered literally (space, comma, period)
with no knowledge highlighting. `item_key` is used for canonical acquisition
signals and definition lookup. `surface_key` is used with `item_key` to look up
the exact displayed form's reader level.

---

### `reader_surface_levels`

The learner's self-rating for an exact displayed form of a canonical item. This
lets a lemma-keyed language track `πηγαίνω`, `πήγα`, and `πηγαίνει` as separate
reader colours while preserving one canonical `user_knowledge` row for
acquisition signals.

```
reader_surface_levels
  user_id     TEXT  NOT NULL    FK → users.user_id
  language    TEXT  NOT NULL    FK → languages.code
  item_key    TEXT  NOT NULL    canonical lemma/root/stem key
  surface_key TEXT  NOT NULL    exact-form key from story_tokens.surface_key
  level       TEXT              ""/NULL | "1".."5" | "well_known" | "ignored"
  updated_at  REAL  NOT NULL

  PRIMARY KEY (user_id, language, item_key, surface_key)
```

Explicit lemma/root well-known or ignored marks remain on `user_knowledge.level`
and cover all surface forms in the reader. Ordinary ratings write here.

---

### `story_audio`

Audio generated for a story, with word-level alignment data.

```
story_audio
  audio_id      TEXT  PK
  story_id      TEXT  NOT NULL    FK → stories.story_id
  file_path     TEXT  NOT NULL    legacy column name; stores object key/ref
  duration_ms   INT               total duration
  alignment     JSON              [{position, start_ms, end_ms}, ...]
                                  position matches story_tokens.position
  generated_at  REAL  NOT NULL
```

`file_path` should be interpreted as a media object key such as
`story_audio/{story_id}/{audio_id}.mp3`. It must not store an absolute local
path, a public URL, or an S3 provider-specific URL. The configured media
`ObjectStore` maps that key to local filesystem storage or S3-compatible storage.

`alignment` maps each word token to a time range in the audio file. The reader
uses this to highlight the currently-playing word during playback, and listening
tasks use it to play specific sentence segments.

---

### `sessions`

A learning session groups a story with its associated tasks. It is the top-level
unit of a user's learning activity.

```
sessions
  session_id          TEXT  PK
  user_id             TEXT  NOT NULL    FK → users.user_id
  story_id            TEXT              FK → stories.story_id, set after generation
  language            TEXT  NOT NULL
  level               TEXT  NOT NULL
  selected_targets    JSON              item_ids that were in the targets[] bucket
  selected_new        JSON              item_ids that were in the new[] bucket
  task_source_text    TEXT              optional selected story excerpt for the next task batch
  created_at          REAL  NOT NULL
  reading_started_at  REAL
  completed_at        REAL
```

`selected_targets` and `selected_new` record what the selection layer chose for
the latest task batch. `task_source_text` is null when the full story is used.
Each generated task snapshots its own source and targets, so later batches can
focus on another passage without changing older tasks or their retries.

---

### `tasks`

Individual tasks generated for a session. All type-specific data lives in JSON
columns so new task types require no schema changes.

```
tasks
  task_id          TEXT  PK
  session_id       TEXT  NOT NULL    FK → sessions.session_id
  user_id          TEXT  NOT NULL    FK → users.user_id
  task_type        TEXT  NOT NULL    registered task type ID, e.g. "comprehension_mc",
                                     "fill_blank", "listen_transcribe", "production"
  language         TEXT  NOT NULL
  source_text      TEXT              immutable story/phrase excerpt used to generate this task
  content          JSON  NOT NULL    task question/prompt; schema owned by task type
  response         JSON              user's answer; schema owned by task type; null until submitted
  input_method     TEXT              "typed" | "scanned_image" | "audio_recording"
  media_path       TEXT              legacy column name; media object key/ref, if applicable
  grade            JSON              grading result; schema owned by task type; null until graded
  graded_by        TEXT              "rule" | "llm"
  graded_at        REAL
  created_at       REAL  NOT NULL
```

`media_path` stores the same object-key shape as `story_audio.file_path`, for
example `task_media/{task_id}/{upload_id}.jpg` or
`conversation_audio/{conversation_id}/{upload_id}.webm`.

**Example content blobs by task type:**

```json
// comprehension_mc
{ "question": "Τί ἐποίησεν ὁ ἄνθρωπος;",
  "options": ["ἔτρεχεν", "ἔπεσεν", "ἔφυγεν", "ἔμεινεν"],
  "correct_index": 1 }

// fill_blank
{ "sentence": "ὁ ἄνθρωπος ___ εἰς τὴν ἀγοράν.",
  "target_item_id": "item_abc123",
  "acceptable_forms": ["ἦλθεν", "ἔβη"] }

// production
{ "prompt_l1": "Write a sentence using the genitive absolute construction.",
  "target_construction_id": "item_xyz789" }

// listen_transcribe
{ "audio_segment": { "audio_id": "aud_123", "start_ms": 4200, "end_ms": 7800 },
  "target_text": "ὁ ἄνθρωπος ἔπεσεν εἰς τὴν θάλασσαν" }
```

---

### `task_targets`

Which knowledge items a task is designed to exercise. Used to update
`user_knowledge` signals after grading, and to track what each session drilled.

```
task_targets
  task_id   TEXT  NOT NULL    FK → tasks.task_id
  item_id   TEXT  NOT NULL    FK → knowledge_items.item_id

  PRIMARY KEY (task_id, item_id)
```

---

### `reader_events`

Every interaction in the reader, logged for signal derivation and predictor
training. High-volume; write-heavy. Archive or partition after 90 days in
production.

```
reader_events
  event_id    TEXT  PK
  user_id     TEXT  NOT NULL
  story_id    TEXT  NOT NULL
  session_id  TEXT
  event_type  TEXT  NOT NULL    "lookup" | "rate" | "navigate" | "sentence_break"
  position    INT               story_tokens.position this event is about
  value       TEXT              for "rate": "1"–"5", "w", "i"; for others: null
  occurred_at REAL  NOT NULL
```

`lookup` events (user pressed Space to see a definition) are the primary raw
source for `user_knowledge.lookup_count`. `rate` events are the source for
knowledge level updates. This table lets us reconstruct the exact sequence of a
reading session for debugging or model training.

---

### `reading_progress`

The current server-backed bookmark for a user and story. This mutable projection
answers “where should the reader resume?”; it is not the append-only analytics
history and does not overload session completion or archive state.

```
reading_progress
  user_id           TEXT  NOT NULL    FK → users.user_id
  story_id          TEXT  NOT NULL    FK → stories.story_id ON DELETE CASCADE
  position          INT   NOT NULL    selectable story_tokens.position
  progress_fraction REAL  NOT NULL    0..1 word-position projection
  finished_at       REAL              explicit Finished reading action
  updated_at        REAL  NOT NULL    server save time

  PRIMARY KEY (user_id, story_id)
```

Ordinary bookmark saves preserve an existing `finished_at`, allowing a learner
to reread or move around a finished story without erasing the historical fact.
User-story edits delete the row because positions from the old tokenization are
not meaningful in the new text.

---

### `conversations`

Session-level state for the adaptive story conversation. The main narrative is
durable; focused repair stories are transcript turns plus a small LIFO stack,
not a graph of curriculum nodes.

```
conversations
  conversation_id TEXT  PK
  user_id         TEXT  NOT NULL
  language        TEXT  NOT NULL    "el" in the first release
  level           TEXT  NOT NULL
  story_summary   TEXT  NOT NULL    compact memory of the main narrative only
  repair_stack    JSON  NOT NULL    [{turn_id, focus}, ...], top frame last
  status          TEXT  NOT NULL    "active" | "complete"
  created_at      REAL  NOT NULL
  updated_at      REAL  NOT NULL
```

The legacy `prompt_text`, `prompt_item_ids`, `audio_path`, `transcript`, and
`analysis` columns remain for migration compatibility but are not the adaptive
loop's source of truth.

### `conversation_turns`

The ordered, append-only conversation transcript. A learner response and the
assistant turn it produces are committed atomically with the corresponding
repair-stack update.

```
conversation_turns
  turn_id          TEXT  PK
  conversation_id  TEXT  NOT NULL    FK → conversations.conversation_id
  turn_index       INT   NOT NULL    unique within the conversation
  role             TEXT  NOT NULL    "assistant" | "user"
  kind             TEXT  NOT NULL    "story" | "repair_story" | "retry" |
                                     "learner_response"
  action           TEXT  NOT NULL    "continue_story" | "descend" |
                                     "retry_parent" (assistant turns)
  assessment       TEXT  NOT NULL    "understood" | "partial" |
                                     "not_understood" (assistant turns)
  greek_text       TEXT  NOT NULL    target passage; future TTS input
  english_text     TEXT  NOT NULL    concise explanation/repair feedback
  prompt_text      TEXT  NOT NULL    question shown after the passage
  input_text       TEXT  NOT NULL    typed learner interpretation
  audio_path       TEXT              future TTS or recorded speech object key
  transcript       TEXT              future STT output
  focus            TEXT  NOT NULL    word/construction isolated by a repair
  reply_to_turn_id TEXT              FK → conversation_turns.turn_id
  created_at       REAL  NOT NULL
```

`audio_path` follows the shared object-key convention, for example
`conversation_audio/{conversation_id}/{turn_id}.mp3`. The API exposes a
short-lived `audio_url`, never this key directly.

---

### `conversation_gaps`

Items the user wanted to express but couldn't during a conversation. These become
highest-priority new items in the selection layer — they represent explicit
learner intent.

```
conversation_gaps
  gap_id          TEXT  PK
  conversation_id TEXT  NOT NULL    FK → conversations.conversation_id
  user_id         TEXT  NOT NULL
  language        TEXT  NOT NULL
  native_text     TEXT              what the user was trying to say (in L1 or described)
  target_phrase   TEXT              best L2 approximation, filled by LLM
  item_id         TEXT              FK → knowledge_items.item_id once created
  status          TEXT  NOT NULL    "pending" | "item_created" | "introduced" | "acquired"
  created_at      REAL  NOT NULL
```

---

### `skills`

Hard-coded per language. Populated at server startup by language plugin
registration. See `skill-system.md`.

```
skills
  skill_id      TEXT  PK
  language      TEXT  NOT NULL    FK → languages.code
  name          TEXT  NOT NULL    e.g. "Genitive Case", "Aorist Tense"
  description   TEXT
  category      TEXT  NOT NULL    grouping for skill tree display
  tier_count    INT   NOT NULL    number of tiers (typically 3)
  xp_per_tier   INT   NOT NULL    XP to cross each tier boundary
  sort_order    INT               display order within category
```

---

### `item_skill_associations`

Materialized lazily from language plugin skill definitions after knowledge items
are created or before graded task targets need XP attribution. Maps knowledge
items to the skills they count toward.

```
item_skill_associations
  item_id       TEXT  NOT NULL    FK → knowledge_items.item_id
  skill_id      TEXT  NOT NULL    FK → skills.skill_id

  PRIMARY KEY (item_id, skill_id)
```

---

### `user_skill_xp`

The user's current XP and tier for each skill.

```
user_skill_xp
  user_id           TEXT  NOT NULL    FK → users.user_id
  skill_id          TEXT  NOT NULL    FK → skills.skill_id
  xp                INT   NOT NULL    DEFAULT 0
  tier              INT   NOT NULL    DEFAULT 0
  pending_verify    BOOL  NOT NULL    DEFAULT false   -- awaiting AI verification
  last_verified_at  REAL
  updated_at        REAL  NOT NULL

  PRIMARY KEY (user_id, skill_id)
```

---

### `task_skill_xp_log`

Append-only log of every XP change, for auditability and predictor training.

```
task_skill_xp_log
  log_id        TEXT  PK
  user_id       TEXT  NOT NULL
  task_id       TEXT  NOT NULL    FK → tasks.task_id
  skill_id      TEXT  NOT NULL    FK → skills.skill_id
  xp_delta      INT   NOT NULL    positive or negative
  xp_after      INT   NOT NULL    user's total XP in this skill after this event
  logged_at     REAL  NOT NULL
```

---

### `session_generation_stages`

One row per pipeline stage per session. Supports intelligent retry from the
failing stage. See `session-types.md`.

```
session_generation_stages
  session_id    TEXT  NOT NULL    FK → sessions.session_id
  stage         TEXT  NOT NULL    'scope_check' | 'story_import' | 'story_generation' |
                                  'tokenization' | 'task_{type_id}'
  status        TEXT  NOT NULL    'pending' | 'in_progress' | 'complete' | 'failed'
  started_at    REAL
  completed_at  REAL
  error_code    TEXT              if failed
  error_detail  TEXT              if failed
  retry_count   INT   NOT NULL    DEFAULT 0

  PRIMARY KEY (session_id, stage)
```

---

### `sessions` (additions)

The `sessions` table gains columns for session type and generation status:

```
sessions (additions to existing table)
  session_type        TEXT  NOT NULL  DEFAULT 'system'
                            'system' | 'topic_guided' | 'expression_guided' |
                            'user_added'
  topic               TEXT            generated topic or user-added story title
  user_expressions    JSON            list of L1 expressions (expression_guided only)
  expression_output   TEXT            'phrases' | 'story' (expression_guided only)
  status              TEXT  NOT NULL  DEFAULT 'pending'
                            'pending' | 'generating' | 'ready' | 'reading' |
                            'complete' | 'failed'
```

---

### `llm_calls`

Every outbound LLM call, for cost tracking, debugging, and prompt version
correlation. Written by the gateway client in `internal/llm/`.

```
llm_calls
  call_id         TEXT  PK
  session_id      TEXT              FK → sessions.session_id, null for non-session calls
  user_id         TEXT
  kind            TEXT  NOT NULL    'story_generator' | 'task_comprehension_mc' |
                                    'grader' | 'assessor' | 'scope_check' | etc.
  prompt_version  TEXT  NOT NULL    builder version string
  model           TEXT  NOT NULL    model identifier used
  input_tokens    INT
  output_tokens   INT
  latency_ms      INT
  status          TEXT  NOT NULL    'success' | 'error' | 'timeout'
  error_detail    TEXT
  called_at       REAL  NOT NULL
```

---

### `story_glossary`

Per-story vocabulary entries for the definition popup. Populated during story
generation: the LLM returns a glossary alongside the story text, covering new
items and any words the generator chose that need definitions available offline.

```
story_glossary
  story_id        TEXT  NOT NULL    FK → stories.story_id
  item_key        TEXT  NOT NULL    canonical knowledge key (lemma/root/stem)
  gloss           TEXT  NOT NULL    short definition in user's native language
  grammatical_note TEXT             e.g. "3rd declension, neuter"
  example         TEXT              example sentence from the story

  PRIMARY KEY (story_id, item_key)
```

This is the first source the reader's definition popup consults. Fallback is the
`knowledge_items.metadata` JSON. A live LLM call is only made if neither source
has the definition.

---

### `breakdowns`

Global, shared cache for exact LLM-backed reader breakdowns. These rows are not
user-scoped: if one learner requests a breakdown, later learners of the same
language can reuse it.

```
breakdowns
  scope       TEXT  NOT NULL    'sentence' | 'word'
  language    TEXT  NOT NULL    FK → languages.code
  cache_key   TEXT  NOT NULL    sentence: hash of normalized sentence text;
                                word: canonical item key
  content     JSON  NOT NULL    served breakdown JSON, prompt-builder-owned
  created_at  REAL  NOT NULL

  PRIMARY KEY (scope, language, cache_key)
```

For sentences this is exact reuse only. Similar sentences do not return another
sentence's cached answer; they use the graph-backed structure tables below as
prompt context for a fresh analysis.

---

### `sentence_structures`

Reusable syntax-graph memory derived from sentence breakdowns. This is the cache
that supports future tree/graph visualization and compositional sentence analysis.

```
sentence_structures
  language             TEXT  NOT NULL    FK → languages.code
  structure_key        TEXT  NOT NULL    hash of the normalized structural template
  template             TEXT  NOT NULL    e.g. "{word} {word}."
  graph                JSON  NOT NULL    SyntaxGraph: token/phrase/clause/sentence
                                        nodes plus dependency-style edges
  phrase_keys          JSON  NOT NULL    cached_phrases keys represented in graph
  source_breakdown_key TEXT              originating exact sentence cache key
  created_at           REAL  NOT NULL
  updated_at           REAL  NOT NULL

  PRIMARY KEY (language, structure_key)
```

The initial structure key is intentionally conservative and deterministic. Later
language plugins or parsers can replace the coarse template with POS/dependency
skeletons without changing the public reader API.

---

### `cached_phrases`

Global, reusable phrase/chunk rows discovered from sentence syntax graphs.

```
cached_phrases
  language             TEXT  NOT NULL    FK → languages.code
  phrase_key           TEXT  NOT NULL    hash(language + kind + normalized_text)
  text                 TEXT  NOT NULL    target-language phrase text
  normalized_text      TEXT  NOT NULL    lookup form for matching future spans
  kind                 TEXT  NOT NULL    phrase | clause | construction | ...
  gloss                TEXT
  notes                TEXT
  graph                JSON  NOT NULL    SyntaxGraph subgraph for this phrase
  metadata             JSON              source annotations, node ids, labels
  source_breakdown_key TEXT              originating exact sentence cache key
  created_at           REAL  NOT NULL
  updated_at           REAL  NOT NULL

  PRIMARY KEY (language, phrase_key)
```

These rows can later be promoted into `knowledge_items` with `item_type = 'phrase'`
or `construction` once the product flow for phrase-level learning is settled.

---

## Indexing Strategy

The following indexes cover the hot query paths. All others are full table scans
over small tables and need no index.

```
-- Selection layer: main read
CREATE INDEX idx_user_knowledge_user_stage
  ON user_knowledge(user_id, acquisition_stage, next_target_after);

-- Reader: token lookup for a story
CREATE INDEX idx_story_tokens_story
  ON story_tokens(story_id, position);

-- Reader events: bulk insert, then aggregate
CREATE INDEX idx_reader_events_user_story
  ON reader_events(user_id, story_id, occurred_at);

-- Reader resume/library ordering
CREATE INDEX idx_reading_progress_user_updated
  ON reading_progress(user_id, updated_at DESC);

-- Task queries per session
CREATE INDEX idx_tasks_session
  ON tasks(session_id);

-- Knowledge items: lookup by language + key
CREATE INDEX idx_knowledge_items_lang_key
  ON knowledge_items(language, key);

-- Predictions: selection layer lookup
CREATE INDEX idx_predictions_user
  ON knowledge_predictions(user_id, computed_at);

-- Skill XP: skill tree view
CREATE INDEX idx_user_skill_xp_user
  ON user_skill_xp(user_id);

-- Skill XP log: per-user history
CREATE INDEX idx_skill_xp_log_user
  ON task_skill_xp_log(user_id, logged_at);

-- LLM calls: cost tracking per user/session
CREATE INDEX idx_llm_calls_session
  ON llm_calls(session_id);
CREATE INDEX idx_llm_calls_user_date
  ON llm_calls(user_id, called_at);

-- Reader syntax cache
CREATE INDEX idx_sentence_structures_language_updated
  ON sentence_structures(language, updated_at);
CREATE INDEX idx_cached_phrases_language_normalized
  ON cached_phrases(language, normalized_text);
```

---

## SQLite vs PostgreSQL Notes

The schema is written to be compatible with both. Constraints:

- Use `TEXT` for all IDs (UUIDs stored as strings — portable, no driver
  differences)
- Use `REAL` for timestamps (Unix seconds as float — works in both)
- Use `JSON` type annotation for JSON columns (SQLite stores as TEXT, Postgres
  uses native JSONB — the repository layer handles the difference)
- No database-level foreign key enforcement in SQLite by default (enforce in
  application layer instead, or `PRAGMA foreign_keys = ON` per connection)
- Full-text search on story text: SQLite FTS5 vs Postgres `tsvector` — abstract
  behind a repository method, implement per-backend

---

## Open Questions

- `reader_events` volume in production: needs partitioning strategy for Postgres
  (partition by month), archival policy for SQLite
- Whether `knowledge_items` should be seeded from a frequency corpus at language
  plugin init, or built up purely from what gets generated
- Sync protocol for desktop SQLite → cloud Postgres: last-write-wins on
  `user_knowledge`, or a proper CRDT for `lookup_count`/`exposure_count` counters
