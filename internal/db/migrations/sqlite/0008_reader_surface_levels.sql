-- Per-surface reader ratings (#43). The canonical user_knowledge row remains
-- the acquisition/predictor signal for a lemma/root/stem. This table stores the
-- learner's visual self-rating for one rendered form of that canonical item.

ALTER TABLE story_tokens ADD COLUMN surface_key TEXT;

CREATE TABLE reader_surface_levels (
    user_id     TEXT NOT NULL REFERENCES users(user_id),
    language    TEXT NOT NULL REFERENCES languages(code),
    item_key    TEXT NOT NULL,
    surface_key TEXT NOT NULL,
    level       TEXT,
    updated_at  REAL NOT NULL,
    PRIMARY KEY (user_id, language, item_key, surface_key)
);

CREATE INDEX idx_reader_surface_levels_user_lang
  ON reader_surface_levels (user_id, language);
