-- Evolve the original one-shot speech placeholder into a durable adaptive
-- story conversation. Legacy columns stay in place so existing rows remain
-- readable; new code writes prompt_text as an empty compatibility value.
ALTER TABLE conversations ADD COLUMN level TEXT NOT NULL DEFAULT 'beginner';
ALTER TABLE conversations ADD COLUMN story_summary TEXT NOT NULL DEFAULT '';
ALTER TABLE conversations ADD COLUMN repair_stack JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE conversations ADD COLUMN status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE conversations ADD COLUMN updated_at DOUBLE PRECISION NOT NULL DEFAULT 0;

CREATE TABLE conversation_turns (
    turn_id          TEXT PRIMARY KEY,
    conversation_id  TEXT NOT NULL REFERENCES conversations(conversation_id) ON DELETE CASCADE,
	turn_index       INTEGER NOT NULL,
    role             TEXT NOT NULL,
    kind             TEXT NOT NULL,
    action           TEXT NOT NULL DEFAULT '',
    assessment       TEXT NOT NULL DEFAULT '',
    greek_text       TEXT NOT NULL DEFAULT '',
    english_text     TEXT NOT NULL DEFAULT '',
    prompt_text      TEXT NOT NULL DEFAULT '',
    input_text       TEXT NOT NULL DEFAULT '',
    audio_path       TEXT,
    transcript       TEXT,
    focus            TEXT NOT NULL DEFAULT '',
    reply_to_turn_id TEXT REFERENCES conversation_turns(turn_id),
    created_at       DOUBLE PRECISION NOT NULL
);

CREATE INDEX idx_conversation_turns_conversation_created
    ON conversation_turns(conversation_id, turn_index);
CREATE UNIQUE INDEX idx_conversation_turns_conversation_index
    ON conversation_turns(conversation_id, turn_index);
CREATE INDEX idx_conversations_user_updated
    ON conversations(user_id, updated_at);
