-- Empty keeps the URL and protocol behavior of existing connections unchanged.
ALTER TABLE provider_credentials ADD COLUMN IF NOT EXISTS api_format TEXT NOT NULL DEFAULT ''
    CHECK (api_format IN ('', 'responses', 'chat_completions'));
