-- Global, shared, cached language resources for the reader's popups. These are
-- NOT user-scoped: a definition or breakdown computed for one learner is reused
-- by every learner of that language. The per-user custom dictionary (#40) layers
-- over this later. See context/reader-mode.md ("The Definition Popup",
-- "Sentence/Word Breakdown") and issues #10/#41/#42.

-- Word definitions, keyed by (language, key) and split by source so a Wiktionary
-- entry and an LLM-written one can coexist for the same word (#41 fills the
-- Wiktionary rows from the kaikki/Wiktextract dataset).
CREATE TABLE definitions (
    language         TEXT NOT NULL REFERENCES languages(code),
    item_key         TEXT NOT NULL,        -- canonical key (lemma/root/stem)
    source           TEXT NOT NULL,        -- 'wiktionary' | 'llm'
    gloss            TEXT NOT NULL,        -- short definition in the learner's L1
    grammatical_note TEXT,
    example          TEXT,
    etymology        TEXT,
    created_at       REAL NOT NULL,
    PRIMARY KEY (language, item_key, source)
);

-- Cached sentence/word breakdowns (LLM-backed, slow, reusable). cache_key is the
-- hash of the normalized sentence text for scope='sentence' (so the same sentence
-- in another story reuses it) and the canonical item_key for scope='word'.
-- content is the breakdown JSON, shape owned by the breakdown prompt builder.
CREATE TABLE breakdowns (
    scope      TEXT NOT NULL,              -- 'sentence' | 'word'
    language   TEXT NOT NULL REFERENCES languages(code),
    cache_key  TEXT NOT NULL,
    content    TEXT NOT NULL,              -- JSON
    created_at REAL NOT NULL,
    PRIMARY KEY (scope, language, cache_key)
);
