-- Track how many times a task has been submitted (#51 re-submission support).
-- Default 1: rows created before this migration count their first submission as attempt 1.

ALTER TABLE tasks ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 1;
