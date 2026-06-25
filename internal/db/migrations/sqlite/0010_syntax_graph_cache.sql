-- Graph-backed sentence structure and phrase cache (#42).
--
-- The public reader endpoint still returns breakdown JSON from breakdowns, but
-- each live sentence breakdown also materializes reusable linguistic structure:
-- a sentence-level syntax graph/template and phrase/subtree rows. These are
-- global language resources, not user-scoped.

CREATE TABLE sentence_structures (
    language             TEXT NOT NULL REFERENCES languages(code),
    structure_key        TEXT NOT NULL,
    template             TEXT NOT NULL,
    graph                TEXT NOT NULL, -- JSON SyntaxGraph
    phrase_keys          TEXT NOT NULL DEFAULT '[]', -- JSON string array
    source_breakdown_key TEXT,
    created_at           REAL NOT NULL,
    updated_at           REAL NOT NULL,
    PRIMARY KEY (language, structure_key)
);

CREATE INDEX idx_sentence_structures_language_updated
  ON sentence_structures(language, updated_at);

CREATE TABLE cached_phrases (
    language             TEXT NOT NULL REFERENCES languages(code),
    phrase_key           TEXT NOT NULL,
    text                 TEXT NOT NULL,
    normalized_text      TEXT NOT NULL,
    kind                 TEXT NOT NULL,
    gloss                TEXT,
    notes                TEXT,
    graph                TEXT NOT NULL, -- JSON SyntaxGraph subgraph
    metadata             TEXT,          -- JSON, source-specific annotations
    source_breakdown_key TEXT,
    created_at           REAL NOT NULL,
    updated_at           REAL NOT NULL,
    PRIMARY KEY (language, phrase_key)
);

CREATE INDEX idx_cached_phrases_language_normalized
  ON cached_phrases(language, normalized_text);
