-- Global, shared, cached language resources for the reader's popups. These are
-- NOT user-scoped: a definition or breakdown computed for one learner is reused
-- by every learner of that language. The per-user custom dictionary (#40) layers
-- over this later. See context/reader-mode.md ("The Definition Popup",
-- "Sentence/Word Breakdown") and issues #10/#41/#42.

CREATE TABLE definitions (
    language         TEXT NOT NULL REFERENCES languages(code),
    item_key         TEXT NOT NULL,
    source           TEXT NOT NULL,        -- 'wiktionary' | 'llm'
    gloss            TEXT NOT NULL,
    grammatical_note TEXT,
    example          TEXT,
    etymology        TEXT,
    created_at       DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (language, item_key, source)
);

CREATE TABLE breakdowns (
    scope      TEXT NOT NULL,              -- 'sentence' | 'word'
    language   TEXT NOT NULL REFERENCES languages(code),
    cache_key  TEXT NOT NULL,
    content    JSONB NOT NULL,
    created_at DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (scope, language, cache_key)
);
