-- tifl canonical schema — SQLite flavour (desktop/local default).
--
-- This is the literal translation of context/database-schema.md. The Postgres
-- variant (JSONB, partitioning for reader_events, tsvector FTS) lives in a
-- parallel migration; the repository layer abstracts the differences.
--
-- Conventions (see context/database-schema.md "SQLite vs PostgreSQL Notes"):
--   * all IDs are TEXT (UUIDs as strings) — portable across drivers
--   * all timestamps are REAL (Unix seconds as float)
--   * JSON columns are TEXT (SQLite stores JSON as text); annotated "-- JSON"
--   * booleans are INTEGER 0/1
--   * FK enforcement requires `PRAGMA foreign_keys = ON;` per connection
-- (schema_migrations and `PRAGMA foreign_keys = ON` are owned by the migration
-- runner in internal/db, not this file.)

-- ---------------------------------------------------------------------------
-- Identity & catalogue
-- ---------------------------------------------------------------------------

CREATE TABLE users (
    user_id       TEXT PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    REAL NOT NULL,
    last_login    REAL,
    settings      TEXT            -- JSON: theme, UI prefs
);

CREATE TABLE languages (
    code         TEXT PRIMARY KEY,        -- "grc", "el", "ar", "zh"
    name         TEXT NOT NULL,
    key_strategy TEXT NOT NULL,           -- surface | lemma | root | stem
    enabled      INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE knowledge_items (
    item_id   TEXT PRIMARY KEY,
    language  TEXT NOT NULL REFERENCES languages(code),
    item_type TEXT NOT NULL,              -- word | phrase | construction | root | idiom | ...
    key       TEXT NOT NULL,              -- canonical key per the language's key strategy
    frequency INTEGER,                    -- rank in the language frequency list (1 = most common)
    metadata  TEXT,                       -- JSON: type-specific, language-plugin-defined
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
    context_variety   INTEGER NOT NULL DEFAULT 0,   -- distinct stories it appeared in
    lookup_count      INTEGER NOT NULL DEFAULT 0,   -- Space presses in the reader (strong "not acquired")
    task_correct      INTEGER NOT NULL DEFAULT 0,
    task_total        INTEGER NOT NULL DEFAULT 0,
    last_seen         REAL,
    last_targeted     REAL,
    confidence_score  REAL,                          -- 0..1, computed by predictor
    next_target_after REAL,                          -- internal SRS-like scheduling; not user-visible
    PRIMARY KEY (user_id, item_id)
);

CREATE TABLE knowledge_predictions (
    user_id           TEXT NOT NULL REFERENCES users(user_id),
    item_id           TEXT NOT NULL REFERENCES knowledge_items(item_id),
    predicted_prob    REAL NOT NULL,                 -- 0..1
    predictor_version TEXT NOT NULL,                 -- "algorithmic-v1" | "ml-v1" | ...
    computed_at       REAL NOT NULL,
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
    estimated_coverage REAL,                          -- predicted-known token fraction at gen time
    generated_at       REAL NOT NULL,
    audio_id           TEXT,                           -- FK story_audio, null until audio generated
    session_id         TEXT
);

CREATE TABLE story_tokens (
    story_id TEXT NOT NULL REFERENCES stories(story_id),
    position INTEGER NOT NULL,             -- 0-indexed, stable id
    surface  TEXT NOT NULL,                -- display form incl. punctuation
    item_key TEXT,                         -- normalized key for lookup; null for non-word tokens
    is_word  INTEGER NOT NULL,             -- 0 for whitespace/punctuation
    PRIMARY KEY (story_id, position)
);

CREATE TABLE story_audio (
    audio_id     TEXT PRIMARY KEY,
    story_id     TEXT NOT NULL REFERENCES stories(story_id),
    file_path    TEXT NOT NULL,            -- S3 key or local path
    duration_ms  INTEGER,
    alignment    TEXT,                     -- JSON: [{position,start_ms,end_ms}, ...]
    generated_at REAL NOT NULL
);

CREATE TABLE sessions (
    session_id         TEXT PRIMARY KEY,
    user_id            TEXT NOT NULL REFERENCES users(user_id),
    story_id           TEXT REFERENCES stories(story_id),
    language           TEXT NOT NULL REFERENCES languages(code),
    level              TEXT NOT NULL,
    selected_targets   TEXT,                           -- JSON: item_ids in targets[]
    selected_new       TEXT,                           -- JSON: item_ids in new[]
    -- session type (context/session-types.md)
    session_type       TEXT NOT NULL DEFAULT 'system', -- system | topic_guided | expression_guided
    topic              TEXT,                           -- topic_guided only
    user_expressions   TEXT,                           -- JSON list; expression_guided only
    expression_output  TEXT,                           -- phrases | story; expression_guided only
    status             TEXT NOT NULL DEFAULT 'pending',-- pending|generating|ready|reading|complete|failed
    created_at         REAL NOT NULL,
    reading_started_at REAL,
    completed_at       REAL
);

CREATE TABLE session_generation_stages (
    session_id   TEXT NOT NULL REFERENCES sessions(session_id),
    stage        TEXT NOT NULL,            -- scope_check|story_generation|tokenization|task_{type_id}
    status       TEXT NOT NULL,            -- pending|in_progress|complete|failed
    started_at   REAL,
    completed_at REAL,
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
    task_type    TEXT NOT NULL,            -- registered TaskType id
    language     TEXT NOT NULL REFERENCES languages(code),
    content      TEXT NOT NULL,            -- JSON, owned by the task type
    response     TEXT,                     -- JSON, owned by the task type; null until submitted
    input_method TEXT,                     -- typed | scanned_image | audio_recording
    media_path   TEXT,
    grade        TEXT,                     -- JSON, owned by the task type; null until graded
    graded_by    TEXT,                     -- rule | llm
    graded_at    REAL,
    created_at   REAL NOT NULL
);

CREATE TABLE task_targets (
    task_id TEXT NOT NULL REFERENCES tasks(task_id),
    item_id TEXT NOT NULL REFERENCES knowledge_items(item_id),
    PRIMARY KEY (task_id, item_id)
);

-- ---------------------------------------------------------------------------
-- Reader signals (high-volume; partition/archive in Postgres)
-- ---------------------------------------------------------------------------

CREATE TABLE reader_events (
    event_id    TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(user_id),
    story_id    TEXT NOT NULL REFERENCES stories(story_id),
    session_id  TEXT,
    event_type  TEXT NOT NULL,             -- lookup | rate | navigate | sentence_break
    position    INTEGER,                   -- story_tokens.position
    value       TEXT,                      -- rate: "1".."5","w","i"; else null
    occurred_at REAL NOT NULL
);

-- ---------------------------------------------------------------------------
-- Conversation practice (speech modality)
-- ---------------------------------------------------------------------------

CREATE TABLE conversations (
    conversation_id TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES users(user_id),
    language        TEXT NOT NULL REFERENCES languages(code),
    prompt_text     TEXT NOT NULL,
    prompt_item_ids TEXT,                  -- JSON
    audio_path      TEXT,
    transcript      TEXT,
    analysis        TEXT,                  -- JSON: {used_correctly, struggled_with, gaps}
    created_at      REAL NOT NULL
);

CREATE TABLE conversation_gaps (
    gap_id          TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
    user_id         TEXT NOT NULL REFERENCES users(user_id),
    language        TEXT NOT NULL REFERENCES languages(code),
    native_text     TEXT,
    target_phrase   TEXT,
    item_id         TEXT REFERENCES knowledge_items(item_id),
    status          TEXT NOT NULL,         -- pending | item_created | introduced | acquired
    created_at      REAL NOT NULL
);

-- ---------------------------------------------------------------------------
-- Skills (context/skill-system.md)
-- ---------------------------------------------------------------------------

CREATE TABLE skills (
    skill_id    TEXT PRIMARY KEY,
    language    TEXT NOT NULL REFERENCES languages(code),
    name        TEXT NOT NULL,
    description TEXT,
    category    TEXT NOT NULL,             -- grouping for the skill-tree view
    tier_count  INTEGER NOT NULL,          -- typically 3
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
    pending_verify   INTEGER NOT NULL DEFAULT 0,  -- crossed a threshold, awaiting AI verification
    last_verified_at REAL,
    updated_at       REAL NOT NULL,
    PRIMARY KEY (user_id, skill_id)
);

CREATE TABLE task_skill_xp_log (
    log_id    TEXT PRIMARY KEY,
    user_id   TEXT NOT NULL REFERENCES users(user_id),
    task_id   TEXT NOT NULL REFERENCES tasks(task_id),
    skill_id  TEXT NOT NULL REFERENCES skills(skill_id),
    xp_delta  INTEGER NOT NULL,
    xp_after  INTEGER NOT NULL,
    logged_at REAL NOT NULL
);

-- ---------------------------------------------------------------------------
-- Observability & per-story glossary
-- ---------------------------------------------------------------------------

CREATE TABLE llm_calls (
    call_id        TEXT PRIMARY KEY,
    session_id     TEXT,                   -- null for non-session calls
    user_id        TEXT,
    kind           TEXT NOT NULL,          -- story_generator | task_* | grader | assessor | scope_check
    prompt_version TEXT NOT NULL,
    model          TEXT NOT NULL,
    input_tokens   INTEGER,
    output_tokens  INTEGER,
    latency_ms     INTEGER,
    status         TEXT NOT NULL,          -- success | error | timeout
    error_detail   TEXT,
    called_at      REAL NOT NULL
);

CREATE TABLE story_glossary (
    story_id         TEXT NOT NULL REFERENCES stories(story_id),
    item_key         TEXT NOT NULL,        -- canonical key
    gloss            TEXT NOT NULL,        -- short definition in the user's L1
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
