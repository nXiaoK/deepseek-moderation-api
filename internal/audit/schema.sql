CREATE TABLE IF NOT EXISTS admin_users (username TEXT PRIMARY KEY, password_hash TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS admin_sessions (token_hash TEXT PRIMARY KEY, username TEXT NOT NULL REFERENCES admin_users(username), csrf TEXT NOT NULL, expires_at TIMESTAMPTZ NOT NULL);
CREATE TABLE IF NOT EXISTS provider_credentials (id TEXT PRIMARY KEY, name TEXT NOT NULL, masked TEXT NOT NULL, encrypted BYTEA NOT NULL, active BOOLEAN NOT NULL DEFAULT TRUE);
CREATE TABLE IF NOT EXISTS audit_policies (id TEXT PRIMARY KEY, name TEXT NOT NULL, alias TEXT UNIQUE NOT NULL, enabled BOOLEAN NOT NULL DEFAULT FALSE, config JSONB NOT NULL, revision BIGINT NOT NULL DEFAULT 1, updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
CREATE TABLE IF NOT EXISTS audit_model_channels (id TEXT PRIMARY KEY, name TEXT NOT NULL, model TEXT NOT NULL, credential_id TEXT NOT NULL REFERENCES provider_credentials(id), timeout_ms INTEGER NOT NULL, max_tokens INTEGER NOT NULL, max_concurrency INTEGER NOT NULL, enabled BOOLEAN NOT NULL DEFAULT TRUE, revision BIGINT NOT NULL DEFAULT 1, cache_epoch TEXT NOT NULL);
ALTER TABLE audit_model_channels ADD COLUMN IF NOT EXISTS text_only BOOLEAN NOT NULL DEFAULT FALSE;
CREATE TABLE IF NOT EXISTS client_api_keys (id TEXT PRIMARY KEY, name TEXT NOT NULL, prefix TEXT NOT NULL, token_hash TEXT UNIQUE NOT NULL, policy_ids JSONB NOT NULL, rpm INTEGER NOT NULL CHECK(rpm BETWEEN 1 AND 10000), active BOOLEAN NOT NULL DEFAULT TRUE, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
ALTER TABLE client_api_keys ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
CREATE TABLE IF NOT EXISTS audit_requests (id TEXT PRIMARY KEY, kind TEXT NOT NULL, policy_id TEXT NOT NULL, client_id TEXT NOT NULL, flagged BOOLEAN NOT NULL, error_code TEXT NOT NULL, metadata JSONB NOT NULL, input_cipher BYTEA, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), expires_at TIMESTAMPTZ NOT NULL);
CREATE INDEX IF NOT EXISTS audit_requests_created ON audit_requests(created_at DESC);
CREATE INDEX IF NOT EXISTS audit_requests_expiry ON audit_requests(expires_at);
CREATE TABLE IF NOT EXISTS admin_action_logs (id BIGSERIAL PRIMARY KEY, username TEXT NOT NULL, action TEXT NOT NULL, resource_id TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
CREATE TABLE IF NOT EXISTS rate_limits (bucket TEXT PRIMARY KEY, starts_at TIMESTAMPTZ NOT NULL, hits INTEGER NOT NULL);

-- Cost records outlive the configurable prompt-log retention period.
CREATE TABLE IF NOT EXISTS model_prices (
 id BIGSERIAL PRIMARY KEY, model TEXT NOT NULL, rates JSONB NOT NULL,
 source TEXT NOT NULL, effective_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 author TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS model_prices_current ON model_prices(model,effective_at DESC,id DESC);
INSERT INTO model_prices(model,rates,source,effective_at,author)
SELECT 'deepseek-flash','{"off_hit":20000,"off_miss":1000000,"off_output":4000000,"peak_hit":40000,"peak_miss":2000000,"peak_output":8000000}'::jsonb,
 'https://api-docs.deepseek.com/zh-cn/quick_start/pricing/ (checked 2026-09-12)','2026-09-12T00:00:00+08:00','system'
WHERE NOT EXISTS(SELECT 1 FROM model_prices WHERE model='deepseek-flash');
INSERT INTO model_prices(model,rates,source,effective_at,author)
SELECT 'deepseek-v4-pro','{"off_hit":150000,"off_miss":4500000,"off_output":13500000,"peak_hit":300000,"peak_miss":9000000,"peak_output":27000000}'::jsonb,
 'https://api-docs.deepseek.com/zh-cn/quick_start/pricing/ (checked 2026-09-12)','2026-09-12T00:00:00+08:00','system'
WHERE NOT EXISTS(SELECT 1 FROM model_prices WHERE model='deepseek-v4-pro');
CREATE TABLE IF NOT EXISTS client_budgets (
 client_id TEXT PRIMARY KEY REFERENCES client_api_keys(id), daily_limit BIGINT CHECK(daily_limit>=0),
 monthly_limit BIGINT CHECK(monthly_limit>=0), revision BIGINT NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS audit_costs (
 id TEXT PRIMARY KEY, request_id TEXT NOT NULL, channel_id TEXT NOT NULL DEFAULT '', client_id TEXT NOT NULL, kind TEXT NOT NULL, policy_id TEXT NOT NULL,
 model TEXT NOT NULL, price_id BIGINT REFERENCES model_prices(id),
 price_snapshot JSONB, started_at TIMESTAMPTZ NOT NULL, budget_date DATE NOT NULL,
 reserved_pico BIGINT NOT NULL CHECK(reserved_pico>=0), amount_pico BIGINT CHECK(amount_pico>=0),
 status TEXT NOT NULL, usage JSONB NOT NULL DEFAULT '{}', settled_at TIMESTAMPTZ,
 note TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS audit_costs_budget ON audit_costs(client_id,budget_date);
CREATE INDEX IF NOT EXISTS audit_costs_time ON audit_costs(started_at DESC);
ALTER TABLE audit_costs ADD COLUMN IF NOT EXISTS latency_ms BIGINT CHECK(latency_ms >= 0);
ALTER TABLE audit_costs ADD COLUMN IF NOT EXISTS request_sent BOOLEAN;
ALTER TABLE audit_costs ADD COLUMN IF NOT EXISTS tariff_period TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS cost_adjustments (
 id BIGSERIAL PRIMARY KEY, cost_id TEXT NOT NULL REFERENCES audit_costs(id),
 previous_status TEXT NOT NULL, previous_amount BIGINT, amount_pico BIGINT NOT NULL,
 reason TEXT NOT NULL, author TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS assessment_cache (
 cache_key TEXT PRIMARY KEY, assessment JSONB NOT NULL, expires_at TIMESTAMPTZ NOT NULL, actual_model TEXT NOT NULL
);

-- Provider bindings are immutable; create another credential to change origin.
ALTER TABLE provider_credentials ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT 'deepseek';
ALTER TABLE provider_credentials ADD COLUMN IF NOT EXISTS base_url TEXT NOT NULL DEFAULT 'https://api.deepseek.com';
ALTER TABLE audit_costs ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT 'deepseek';

CREATE INDEX IF NOT EXISTS audit_costs_request ON audit_costs(request_id);
