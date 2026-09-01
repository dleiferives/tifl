-- Durable reader bookmark and explicit story-finished state. This is a
-- current-state projection; append-only reading activity belongs in the
-- learning event log rather than this row.
CREATE TABLE reading_progress (
    user_id           TEXT NOT NULL REFERENCES users(user_id),
    story_id          TEXT NOT NULL REFERENCES stories(story_id) ON DELETE CASCADE,
    position          INTEGER NOT NULL CHECK (position >= 0),
    progress_fraction REAL NOT NULL DEFAULT 0 CHECK (progress_fraction >= 0 AND progress_fraction <= 1),
    finished_at       REAL,
    updated_at        REAL NOT NULL,
    PRIMARY KEY (user_id, story_id)
);

CREATE INDEX idx_reading_progress_user_updated
  ON reading_progress (user_id, updated_at DESC);
