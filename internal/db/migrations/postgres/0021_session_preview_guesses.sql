CREATE TABLE session_preview_guesses (
    session_id  TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    user_id     TEXT NOT NULL REFERENCES users(user_id),
    item_id     TEXT NOT NULL REFERENCES knowledge_items(item_id),
    guess_kind  TEXT NOT NULL,
    guess_text  TEXT,
    correct     BOOLEAN,
    created_at  DOUBLE PRECISION NOT NULL,
    updated_at  DOUBLE PRECISION,
    PRIMARY KEY (session_id, item_id)
);

CREATE INDEX idx_session_preview_guesses_user_session
    ON session_preview_guesses(user_id, session_id);
