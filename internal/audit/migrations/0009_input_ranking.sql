ALTER TABLE audit_requests ADD COLUMN input_fingerprint TEXT CHECK (input_fingerprint ~ '^[a-f0-9]{64}$');
ALTER TABLE audit_requests ADD COLUMN input_fingerprint_checked BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX audit_requests_input_fingerprint ON audit_requests(input_fingerprint,created_at DESC) WHERE input_fingerprint IS NOT NULL;
CREATE INDEX audit_requests_input_backfill ON audit_requests(created_at,id) WHERE NOT input_fingerprint_checked AND octet_length(input_cipher)>0 AND kind='production';
