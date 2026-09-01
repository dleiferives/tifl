-- Keep the learner's chosen subject or pasted source attached to the durable
-- conversation so resumed turns remain anchored to the same material.
ALTER TABLE conversations ADD COLUMN topic TEXT NOT NULL DEFAULT '';
ALTER TABLE conversations ADD COLUMN source_text TEXT NOT NULL DEFAULT '';
