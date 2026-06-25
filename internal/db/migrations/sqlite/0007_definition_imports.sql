-- Audit trail for offline Wiktextract/kaikki imports. Definition lookup remains
-- keyed by definitions(language, item_key, source); this table records which
-- dataset refresh populated or replaced those rows.
CREATE TABLE definition_imports (
    import_id           TEXT PRIMARY KEY,
    language            TEXT NOT NULL REFERENCES languages(code),
    source              TEXT NOT NULL,
    source_path         TEXT NOT NULL,
    dataset_version     TEXT,
    started_at          REAL NOT NULL,
    completed_at        REAL,
    status              TEXT NOT NULL,
    entries_read        INTEGER NOT NULL DEFAULT 0,
    entries_matched     INTEGER NOT NULL DEFAULT 0,
    definitions_written INTEGER NOT NULL DEFAULT 0,
    error               TEXT
);

CREATE INDEX idx_definition_imports_language_started
  ON definition_imports(language, started_at);
