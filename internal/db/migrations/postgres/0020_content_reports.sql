CREATE TABLE content_reports (
    report_id           TEXT PRIMARY KEY,
    reporter_user_id    TEXT NOT NULL REFERENCES users(user_id),
    kind                TEXT NOT NULL,
    target_id           TEXT NOT NULL,
    context_kind        TEXT NOT NULL,
    context_id          TEXT NOT NULL,
    reason_category     TEXT NOT NULL,
    note                TEXT,
    snapshot            JSONB NOT NULL,
    outcome             TEXT NOT NULL,
    outcome_detail      TEXT,
    replacement_task_id TEXT,
    created_at          DOUBLE PRECISION NOT NULL,
    updated_at          DOUBLE PRECISION
);

CREATE INDEX idx_content_reports_target
    ON content_reports(kind, target_id, created_at);

CREATE INDEX idx_content_reports_context
    ON content_reports(context_kind, context_id, kind, outcome, created_at);
