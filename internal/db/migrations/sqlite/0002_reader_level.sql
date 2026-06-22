-- The reader's user-facing knowledge "level" for an item — the self-rating the
-- learner assigns in the reader and the colour the word is painted with. It is
-- deliberately separate from acquisition_stage: stage is the system-computed
-- state the selector/predictor reason about, whereas level is the learner's own
-- assertion ("1".."5" | "well_known" | "ignored"). NULL = unseen / never rated.
-- See context/reader-mode.md ("Visual Encoding of Knowledge Levels").
ALTER TABLE user_knowledge ADD COLUMN level TEXT;
