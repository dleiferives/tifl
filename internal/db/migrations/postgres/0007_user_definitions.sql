-- Per-user custom dictionary entries. These are the learner-owned layer over
-- the global shared definition cache; lookups check this table before glossary,
-- metadata, Wiktionary, or LLM sources.
CREATE TABLE user_definitions (
    user_id    TEXT NOT NULL REFERENCES users(user_id),
    language   TEXT NOT NULL REFERENCES languages(code),
    item_key   TEXT NOT NULL,
    gloss      TEXT NOT NULL,
    notes      TEXT,
    created_at DOUBLE PRECISION NOT NULL,
    updated_at DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (user_id, language, item_key)
);

CREATE INDEX idx_user_definitions_user_lang
  ON user_definitions(user_id, language);
