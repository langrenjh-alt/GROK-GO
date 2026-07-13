CREATE TABLE admins (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    totp_secret_cipher BYTEA,
    totp_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    pending_totp_secret_cipher BYTEA,
    pending_totp_expires_at TIMESTAMPTZ,
    disabled BOOLEAN NOT NULL DEFAULT FALSE,
    last_login_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT admins_email_normalized CHECK (email = lower(btrim(email)))
);

CREATE UNIQUE INDEX admins_email_unique ON admins (lower(email));

CREATE TABLE admin_sessions (
    id TEXT PRIMARY KEY,
    admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    token_digest BYTEA NOT NULL UNIQUE,
    csrf_digest BYTEA NOT NULL,
    ip_address TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX admin_sessions_admin_id_idx ON admin_sessions (admin_id);
CREATE INDEX admin_sessions_expires_at_idx ON admin_sessions (expires_at);

CREATE TABLE proxies (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    url_cipher BYTEA NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    healthy BOOLEAN NOT NULL DEFAULT FALSE,
    last_checked_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE accounts (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    tier TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active',
    email TEXT NOT NULL DEFAULT '',
    credential_cipher BYTEA NOT NULL,
    proxy_id TEXT REFERENCES proxies(id) ON DELETE SET NULL,
    models JSONB NOT NULL DEFAULT '[]'::jsonb,
    tags JSONB NOT NULL DEFAULT '[]'::jsonb,
    priority INTEGER NOT NULL DEFAULT 0,
    concurrency_limit INTEGER NOT NULL DEFAULT 1,
    quota JSONB NOT NULL DEFAULT '{}'::jsonb,
    cooldown_until TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT accounts_kind_check CHECK (kind IN ('cli_oauth', 'console_sso', 'grok_sso')),
    CONSTRAINT accounts_status_check CHECK (status IN ('active', 'cooldown', 'expired', 'disabled', 'error')),
    CONSTRAINT accounts_concurrency_check CHECK (concurrency_limit > 0)
);

CREATE INDEX accounts_schedulable_idx ON accounts (status, priority DESC, updated_at);
CREATE INDEX accounts_proxy_id_idx ON accounts (proxy_id);
CREATE INDEX accounts_models_gin_idx ON accounts USING GIN (models);
CREATE INDEX accounts_tags_gin_idx ON accounts USING GIN (tags);

CREATE TABLE models (
    id TEXT PRIMARY KEY,
    upstream_model TEXT NOT NULL,
    display_name TEXT NOT NULL,
    capability TEXT NOT NULL,
    credential_kinds JSONB NOT NULL DEFAULT '[]'::jsonb,
    minimum_tier TEXT NOT NULL DEFAULT '',
    aliases JSONB NOT NULL DEFAULT '[]'::jsonb,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT models_capability_check CHECK (capability IN ('chat', 'responses', 'messages', 'image', 'video'))
);

CREATE INDEX models_enabled_capability_idx ON models (enabled, capability);
CREATE INDEX models_aliases_gin_idx ON models USING GIN (aliases);

CREATE TABLE client_keys (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    prefix TEXT NOT NULL,
    digest BYTEA NOT NULL UNIQUE,
    secret_cipher BYTEA,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    rpm INTEGER NOT NULL DEFAULT 0,
    concurrency_limit INTEGER NOT NULL DEFAULT 0,
    daily_request_limit BIGINT NOT NULL DEFAULT 0,
    monthly_token_limit BIGINT NOT NULL DEFAULT 0,
    last_used_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT client_keys_limits_check CHECK (rpm >= 0 AND concurrency_limit >= 0 AND daily_request_limit >= 0 AND monthly_token_limit >= 0)
);

CREATE INDEX client_keys_prefix_idx ON client_keys (prefix);
CREATE INDEX client_keys_active_idx ON client_keys (enabled, expires_at);

CREATE TABLE request_logs (
    id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL,
    client_key_id TEXT REFERENCES client_keys(id) ON DELETE SET NULL,
    account_id TEXT REFERENCES accounts(id) ON DELETE SET NULL,
    model TEXT NOT NULL DEFAULT '',
    endpoint TEXT NOT NULL,
    status_code INTEGER NOT NULL,
    duration_ms BIGINT NOT NULL DEFAULT 0,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cached_tokens BIGINT NOT NULL DEFAULT 0,
    error_code TEXT NOT NULL DEFAULT '',
    error_summary TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT request_logs_status_check CHECK (status_code >= 0 AND status_code <= 599)
);

CREATE INDEX request_logs_created_at_idx ON request_logs (created_at DESC);
CREATE INDEX request_logs_request_id_idx ON request_logs (request_id);
CREATE INDEX request_logs_client_key_idx ON request_logs (client_key_id, created_at DESC);
CREATE INDEX request_logs_account_idx ON request_logs (account_id, created_at DESC);
CREATE INDEX request_logs_model_idx ON request_logs (model, created_at DESC);
