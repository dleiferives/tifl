-- IPA pronunciation, related forms, and derived expressions extracted from
-- Wiktextract/kaikki. All nullable: not every entry has all three. Both
-- English and native Wiktionary imports populate these from the same fields
-- in the kaikki JSONL; whichever source has the data wins per source row.
ALTER TABLE definitions ADD COLUMN pronunciation TEXT;
ALTER TABLE definitions ADD COLUMN related       TEXT;
ALTER TABLE definitions ADD COLUMN derived       TEXT;
