-- tifl canonical schema — PostgreSQL flavour (cloud/SaaS mode).
--
-- Parallel translation of internal/db/migrations/sqlite/0001_init.sql. The
-- repository layer (internal/db) abstracts the differences so handlers and
-- domain logic are backend-agnostic. See context/database-schema.md
-- ("SQLite vs PostgreSQL Notes") and context/backend-server.md.
--
-- Conventions (mirroring the SQLite flavour, with native Postgres types):
--   * all IDs are TEXT (UUIDs as strings) — portable across drivers
--   * all timestamps are DOUBLE PRECISION (Unix seconds as float), matching
--     the SQLite REAL columns so the repository code is identical
--   * JSON columns are JSONB (native, indexable)
--   * booleans stay INTEGER 0/1 for parity with the SQLite scan path
--   * foreign keys are enforced natively (no per-connection pragma)
-- (schema_migrations bookkeeping is owned by the pgx migration runner in
-- internal/db/postgres.go, not this file.)

-- ---------------------------------------------------------------------------
-- Identity & catalogue
-- ---------------------------------------------------------------------------

CREATE TABLE users (
    user_id       TEXT PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    DOUBLE PRECISION NOT NULL,
    last_login    DOUBLE PRECISION,
    settings      JSONB
);

CREATE TABLE languages (
    code         TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    key_strategy TEXT NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE knowledge_items (
    item_id   TEXT PRIMARY KEY,
    language  TEXT NOT NULL REFERENCES languages(code),
    item_type TEXT NOT NULL,
    key       TEXT NOT NULL,
    frequency INTEGER,
    metadata  JSONB,
    UNIQUE (language, item_type, key)
);

-- ---------------------------------------------------------------------------
-- Knowledge model
-- ---------------------------------------------------------------------------

CREATE TABLE user_knowledge (
    user_id           TEXT NOT NULL REFERENCES users(user_id),
    item_id           TEXT NOT NULL REFERENCES knowledge_items(item_id),
    acquisition_stage TEXT NOT NULL DEFAULT 'unseen',
    exposure_count    INTEGER NOT NULL DEFAULT 0,
    context_variety   INTEGER NOT NULL DEFAULT 0,
    lookup_count      INTEGER NOT NULL DEFAULT 0,
    task_correct      INTEGER NOT NULL DEFAULT 0,
    task_total        INTEGER NOT NULL DEFAULT 0,
    last_seen         DOUBLE PRECISION,
    last_targeted     DOUBLE PRECISION,
    confidence_score  DOUBLE PRECISION,
    next_target_after DOUBLE PRECISION,
    PRIMARY KEY (user_id, item_id)
);

CREATE TABLE knowledge_predictions (
    user_id           TEXT NOT NULL REFERENCES users(user_id),
    item_id           TEXT NOT NULL REFERENCES knowledge_items(item_id),
    predicted_prob    DOUBLE PRECISION NOT NULL,
    predictor_version TEXT NOT NULL,
    computed_at       DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (user_id, item_id)
);

-- ---------------------------------------------------------------------------
-- Stories & sessions
-- ---------------------------------------------------------------------------

CREATE TABLE stories (
    story_id           TEXT PRIMARY KEY,
    user_id            TEXT NOT NULL REFERENCES users(user_id),
    language           TEXT NOT NULL REFERENCES languages(code),
    text               TEXT NOT NULL,
    level              TEXT NOT NULL,
    topic              TEXT,
    estimated_coverage DOUBLE PRECISION,
    generated_at       DOUBLE PRECISION NOT NULL,
    audio_id           TEXT,
    session_id         TEXT
);

CREATE TABLE story_tokens (
    story_id TEXT NOT NULL REFERENCES stories(story_id),
    position INTEGER NOT NULL,
    surface  TEXT NOT NULL,
    item_key TEXT,
    is_word  INTEGER NOT NULL,
    PRIMARY KEY (story_id, position)
);

CREATE TABLE story_audio (
    audio_id     TEXT PRIMARY KEY,
    story_id     TEXT NOT NULL REFERENCES stories(story_id),
    file_path    TEXT NOT NULL,            -- media object key/ref
    duration_ms  INTEGER,
    alignment    JSONB,
    generated_at DOUBLE PRECISION NOT NULL
);

CREATE TABLE sessions (
    session_id         TEXT PRIMARY KEY,
    user_id            TEXT NOT NULL REFERENCES users(user_id),
    story_id           TEXT REFERENCES stories(story_id),
    language           TEXT NOT NULL REFERENCES languages(code),
    level              TEXT NOT NULL,
    selected_targets   JSONB,
    selected_new       JSONB,
    session_type       TEXT NOT NULL DEFAULT 'system', -- system | topic_guided | expression_guided | user_added
    topic              TEXT,                           -- generated topic or user-added title
    user_expressions   JSONB,
    expression_output  TEXT,
    status             TEXT NOT NULL DEFAULT 'pending',
    created_at         DOUBLE PRECISION NOT NULL,
    reading_started_at DOUBLE PRECISION,
    completed_at       DOUBLE PRECISION
);

CREATE TABLE session_generation_stages (
    session_id   TEXT NOT NULL REFERENCES sessions(session_id),
    stage        TEXT NOT NULL,
    status       TEXT NOT NULL,
    started_at   DOUBLE PRECISION,
    completed_at DOUBLE PRECISION,
    error_code   TEXT,
    error_detail TEXT,
    retry_count  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (session_id, stage)
);

-- ---------------------------------------------------------------------------
-- Tasks
-- ---------------------------------------------------------------------------

CREATE TABLE tasks (
    task_id      TEXT PRIMARY KEY,
    session_id   TEXT NOT NULL REFERENCES sessions(session_id),
    user_id      TEXT NOT NULL REFERENCES users(user_id),
    task_type    TEXT NOT NULL,
    language     TEXT NOT NULL REFERENCES languages(code),
    content      JSONB NOT NULL,
    response     JSONB,
    input_method TEXT,
    media_path   TEXT,                     -- media object key/ref
    grade        JSONB,
    graded_by    TEXT,
    graded_at    DOUBLE PRECISION,
    created_at   DOUBLE PRECISION NOT NULL
);

CREATE TABLE task_targets (
    task_id TEXT NOT NULL REFERENCES tasks(task_id),
    item_id TEXT NOT NULL REFERENCES knowledge_items(item_id),
    PRIMARY KEY (task_id, item_id)
);

-- ---------------------------------------------------------------------------
-- Reader signals (high-volume; range-partitioned by month in Postgres)
-- ---------------------------------------------------------------------------
--
-- Declarative range partitioning on occurred_at (Unix seconds). The partition
-- key must be part of the primary key, hence the composite (event_id,
-- occurred_at). A DEFAULT partition keeps inserts working out of the box; a
-- maintenance job can later attach per-month partitions and the planner will
-- route rows automatically. See context/database-schema.md ("Open Questions").
CREATE TABLE reader_events (
    event_id    TEXT NOT NULL,
    user_id     TEXT NOT NULL REFERENCES users(user_id),
    story_id    TEXT NOT NULL REFERENCES stories(story_id),
    session_id  TEXT,
    event_type  TEXT NOT NULL,
    position    INTEGER,
    value       TEXT,
    occurred_at DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (event_id, occurred_at)
) PARTITION BY RANGE (occurred_at);

CREATE TABLE reader_events_default PARTITION OF reader_events DEFAULT;

-- ---------------------------------------------------------------------------
-- Conversation practice (speech modality)
-- ---------------------------------------------------------------------------

CREATE TABLE conversations (
    conversation_id TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES users(user_id),
    language        TEXT NOT NULL REFERENCES languages(code),
    prompt_text     TEXT NOT NULL,
    prompt_item_ids JSONB,
    audio_path      TEXT,                  -- media object key/ref
    transcript      TEXT,
    analysis        JSONB,
    created_at      DOUBLE PRECISION NOT NULL
);

CREATE TABLE conversation_gaps (
    gap_id          TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
    user_id         TEXT NOT NULL REFERENCES users(user_id),
    language        TEXT NOT NULL REFERENCES languages(code),
    native_text     TEXT,
    target_phrase   TEXT,
    item_id         TEXT REFERENCES knowledge_items(item_id),
    status          TEXT NOT NULL,
    created_at      DOUBLE PRECISION NOT NULL
);

-- ---------------------------------------------------------------------------
-- Skills (context/skill-system.md)
-- ---------------------------------------------------------------------------

CREATE TABLE skills (
    skill_id    TEXT PRIMARY KEY,
    language    TEXT NOT NULL REFERENCES languages(code),
    name        TEXT NOT NULL,
    description TEXT,
    category    TEXT NOT NULL,
    tier_count  INTEGER NOT NULL,
    xp_per_tier INTEGER NOT NULL,
    sort_order  INTEGER
);

CREATE TABLE item_skill_associations (
    item_id  TEXT NOT NULL REFERENCES knowledge_items(item_id),
    skill_id TEXT NOT NULL REFERENCES skills(skill_id),
    PRIMARY KEY (item_id, skill_id)
);

CREATE TABLE user_skill_xp (
    user_id          TEXT NOT NULL REFERENCES users(user_id),
    skill_id         TEXT NOT NULL REFERENCES skills(skill_id),
    xp               INTEGER NOT NULL DEFAULT 0,
    tier             INTEGER NOT NULL DEFAULT 0,
    pending_verify   INTEGER NOT NULL DEFAULT 0,
    last_verified_at DOUBLE PRECISION,
    updated_at       DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (user_id, skill_id)
);

CREATE TABLE task_skill_xp_log (
    log_id    TEXT PRIMARY KEY,
    user_id   TEXT NOT NULL REFERENCES users(user_id),
    task_id   TEXT NOT NULL REFERENCES tasks(task_id),
    skill_id  TEXT NOT NULL REFERENCES skills(skill_id),
    xp_delta  INTEGER NOT NULL,
    xp_after  INTEGER NOT NULL,
    logged_at DOUBLE PRECISION NOT NULL
);

-- ---------------------------------------------------------------------------
-- Observability & per-story glossary
-- ---------------------------------------------------------------------------

CREATE TABLE llm_calls (
    call_id        TEXT PRIMARY KEY,
    session_id     TEXT,
    user_id        TEXT,
    kind           TEXT NOT NULL,
    prompt_version TEXT NOT NULL,
    model          TEXT NOT NULL,
    input_tokens   INTEGER,
    output_tokens  INTEGER,
    latency_ms     INTEGER,
    status         TEXT NOT NULL,
    error_detail   TEXT,
    called_at      DOUBLE PRECISION NOT NULL
);

CREATE TABLE story_glossary (
    story_id         TEXT NOT NULL REFERENCES stories(story_id),
    item_key         TEXT NOT NULL,
    gloss            TEXT NOT NULL,
    grammatical_note TEXT,
    example          TEXT,
    PRIMARY KEY (story_id, item_key)
);

-- ---------------------------------------------------------------------------
-- Indexes (hot query paths only)
-- ---------------------------------------------------------------------------

CREATE INDEX idx_user_knowledge_user_stage  ON user_knowledge (user_id, acquisition_stage, next_target_after);
CREATE INDEX idx_story_tokens_story         ON story_tokens (story_id, position);
CREATE INDEX idx_reader_events_user_story   ON reader_events (user_id, story_id, occurred_at);
CREATE INDEX idx_tasks_session              ON tasks (session_id);
CREATE INDEX idx_knowledge_items_lang_key   ON knowledge_items (language, key);
CREATE INDEX idx_predictions_user           ON knowledge_predictions (user_id, computed_at);
CREATE INDEX idx_user_skill_xp_user         ON user_skill_xp (user_id);
CREATE INDEX idx_skill_xp_log_user          ON task_skill_xp_log (user_id, logged_at);
CREATE INDEX idx_llm_calls_session          ON llm_calls (session_id);
CREATE INDEX idx_llm_calls_user_date        ON llm_calls (user_id, called_at);
