-- Form aliases (inflections/conjugations) store the lemma's item_key here so
-- the definition resolver can follow the link to the lemma's English definition
-- when the form's own gloss is absent or in the target language only.
ALTER TABLE definitions ADD COLUMN canonical_key TEXT;
