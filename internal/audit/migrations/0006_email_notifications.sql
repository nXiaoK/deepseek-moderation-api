CREATE TABLE email_settings (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    revision BIGINT NOT NULL DEFAULT 1,
    config JSONB NOT NULL DEFAULT '{"enabled":false,"host":"","port":465,"security":"tls","username":"","from":"","to":""}',
    password_cipher BYTEA
);
INSERT INTO email_settings(id) VALUES(TRUE);

-- Keep only references: audit retention also removes notification history.
CREATE TABLE email_notifications (
    request_id TEXT PRIMARY KEY REFERENCES audit_requests(id) ON DELETE CASCADE,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    sent_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX email_notifications_pending ON email_notifications(next_attempt_at)
    WHERE sent_at IS NULL AND attempts < 5;
