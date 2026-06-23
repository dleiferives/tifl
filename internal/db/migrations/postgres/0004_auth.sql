CREATE TABLE refresh_tokens (
    token_hash       TEXT PRIMARY KEY,
    family_id        TEXT NOT NULL,
    user_id          TEXT NOT NULL REFERENCES users(user_id),
    issued_at        DOUBLE PRECISION NOT NULL,
    expires_at       DOUBLE PRECISION NOT NULL,
    revoked_at       DOUBLE PRECISION,
    replaced_by_hash TEXT
);

CREATE INDEX idx_refresh_tokens_user
    ON refresh_tokens(user_id);

CREATE INDEX idx_refresh_tokens_family
    ON refresh_tokens(family_id);
