-- Persist prompt/response payloads for session debug views.

ALTER TABLE llm_calls ADD COLUMN system_prompt TEXT;
ALTER TABLE llm_calls ADD COLUMN user_prompt TEXT;
ALTER TABLE llm_calls ADD COLUMN raw_response TEXT;
ALTER TABLE llm_calls ADD COLUMN parsed_output TEXT;
ALTER TABLE llm_calls ADD COLUMN error_payload TEXT;
