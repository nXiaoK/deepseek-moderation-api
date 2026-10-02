CREATE TABLE audit_settings (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    revision BIGINT NOT NULL DEFAULT 1,
    reason_max_chars INTEGER NOT NULL DEFAULT 80 CHECK (reason_max_chars BETWEEN 1 AND 4096)
);
INSERT INTO audit_settings(id) VALUES(TRUE);
