-- FSRS memory state per (user, item): difficulty/stability/last-review for
-- the DSR predictor (#209). NULL/0 = no rated review yet.
ALTER TABLE user_knowledge ADD COLUMN fsrs_difficulty REAL NOT NULL DEFAULT 0;
ALTER TABLE user_knowledge ADD COLUMN fsrs_stability REAL NOT NULL DEFAULT 0;
ALTER TABLE user_knowledge ADD COLUMN fsrs_last_review REAL NOT NULL DEFAULT 0;
