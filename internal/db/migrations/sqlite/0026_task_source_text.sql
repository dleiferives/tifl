-- Snapshot the source passage used to generate each task. Multiple task batches
-- may target different excerpts of one user-added story.
ALTER TABLE tasks ADD COLUMN source_text TEXT;
