-- Expression-guided phrase sets (context/session-types.md "Phrase set").
-- A phrase-set session has no narrative story; its content is a curated list of
-- target-language phrases with annotations, stored as one JSONB row per session.
-- It is not tokenized into story_tokens, so it does not reuse the stories table.
CREATE TABLE session_phrase_sets (
    session_id   TEXT PRIMARY KEY REFERENCES sessions(session_id),
    user_id      TEXT NOT NULL REFERENCES users(user_id),
    language     TEXT NOT NULL REFERENCES languages(code),
    items        JSONB NOT NULL,
    generated_at DOUBLE PRECISION NOT NULL
);
