-- Async signal processing (#210): worker-claimed derivation marker. NULL =
-- signals not yet derived from this event. Historical rows were processed
-- synchronously at insert time, so they are backfilled as processed.
ALTER TABLE reader_events ADD COLUMN processed_at DOUBLE PRECISION;
UPDATE reader_events SET processed_at = occurred_at;
