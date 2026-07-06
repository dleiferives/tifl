ALTER TABLE tasks ADD COLUMN reference_assisted INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_tasks_user_reference_assisted ON tasks(user_id, reference_assisted, graded_at);
