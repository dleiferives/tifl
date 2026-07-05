-- FSRS memory state per (user, item): difficulty/stability/last-review for
-- the DSR predictor (#209). NULL/0 = no rated review yet.
ALTER TABLE user_knowledge ADD COLUMN fsrs_difficulty DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE user_knowledge ADD COLUMN fsrs_stability DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE user_knowledge ADD COLUMN fsrs_last_review DOUBLE PRECISION NOT NULL DEFAULT 0;
