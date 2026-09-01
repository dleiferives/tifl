-- Optional excerpt selected by the learner when generating tasks for an
-- imported story. NULL means task generation should use the whole story.
ALTER TABLE sessions ADD COLUMN task_source_text TEXT;
