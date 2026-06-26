-- Soft-archive sessions without changing their generation/reading status.

ALTER TABLE sessions ADD COLUMN archived_at DOUBLE PRECISION;

CREATE INDEX idx_sessions_user_archived_created ON sessions (user_id, archived_at, created_at, session_id);
