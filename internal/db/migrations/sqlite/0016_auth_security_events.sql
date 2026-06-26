CREATE TABLE auth_security_events (
    event_id              TEXT PRIMARY KEY,
    event_type            TEXT NOT NULL CHECK(event_type <> ''),
    flow                  TEXT NOT NULL CHECK(flow IN ('login', 'register')),
    email_hash            TEXT NOT NULL CHECK(email_hash <> ''),
    source_address_bucket TEXT NOT NULL CHECK(source_address_bucket <> ''),
    user_id               TEXT REFERENCES users(user_id) ON DELETE SET NULL,
    created_at            REAL NOT NULL,
    details               TEXT
);

CREATE INDEX idx_auth_security_events_recent
    ON auth_security_events(created_at DESC, event_id DESC);

CREATE INDEX idx_auth_security_events_user_recent
    ON auth_security_events(user_id, created_at DESC);

CREATE INDEX idx_auth_security_events_email_recent
    ON auth_security_events(email_hash, created_at DESC);

CREATE INDEX idx_auth_security_events_source_recent
    ON auth_security_events(source_address_bucket, created_at DESC);

CREATE INDEX idx_auth_security_events_type_flow_recent
    ON auth_security_events(event_type, flow, created_at DESC);
