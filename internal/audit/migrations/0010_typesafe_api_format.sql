ALTER TABLE provider_credentials DROP CONSTRAINT provider_credentials_api_format_check;
ALTER TABLE provider_credentials ADD CONSTRAINT provider_credentials_api_format_check
    CHECK (api_format IN ('', 'responses', 'chat_completions', 'systemone'));
