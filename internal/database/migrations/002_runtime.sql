CREATE TABLE settings (
    key TEXT PRIMARY KEY,
    value JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE media_objects (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    path TEXT NOT NULL UNIQUE,
    content_type TEXT NOT NULL,
    size BIGINT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT media_objects_kind_check CHECK (kind IN ('image', 'video')),
    CONSTRAINT media_objects_size_check CHECK (size >= 0)
);

CREATE INDEX media_objects_expires_at_idx ON media_objects (expires_at);
CREATE INDEX media_objects_created_at_idx ON media_objects (created_at DESC);

CREATE TABLE video_jobs (
    id TEXT PRIMARY KEY,
    account_id TEXT REFERENCES accounts(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'queued',
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    media_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX video_jobs_status_idx ON video_jobs (status, updated_at DESC);

CREATE TABLE audit_logs (
    id TEXT PRIMARY KEY,
    admin_id TEXT REFERENCES admins(id) ON DELETE SET NULL,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL DEFAULT '',
    ip_address TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX audit_logs_created_at_idx ON audit_logs (created_at DESC);
CREATE INDEX audit_logs_resource_idx ON audit_logs (resource_type, resource_id, created_at DESC);

CREATE TABLE usage_buckets (
    client_key_id TEXT NOT NULL REFERENCES client_keys(id) ON DELETE CASCADE,
    bucket_start TIMESTAMPTZ NOT NULL,
    bucket_kind TEXT NOT NULL,
    requests BIGINT NOT NULL DEFAULT 0,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cached_tokens BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (client_key_id, bucket_kind, bucket_start),
    CONSTRAINT usage_buckets_kind_check CHECK (bucket_kind IN ('minute', 'day', 'month'))
);

INSERT INTO settings (key, value) VALUES
    ('service', '{"service_name":"GROK-GO","timezone":"Asia/Shanghai","default_locale":"zh"}'::jsonb),
    ('retention', '{"request_log_days":30,"image_ttl_hours":72,"video_ttl_hours":168,"media_max_bytes":21474836480}'::jsonb)
ON CONFLICT (key) DO NOTHING;

INSERT INTO models (
    id, upstream_model, display_name, capability, credential_kinds,
    minimum_tier, aliases, enabled
) VALUES
    ('grok-4.5', 'grok-4.5', 'Grok 4.5', 'chat', '["cli_oauth"]', '', '["grok-latest","grok"]', TRUE),
    ('grok-4.3', 'grok-4.3', 'Grok 4.3', 'chat', '["cli_oauth","console_sso"]', '', '[]', TRUE),
    ('grok-build-0.1', 'grok-build-0.1', 'Grok Build', 'chat', '["cli_oauth"]', '', '["grok-build","grok-build-console"]', TRUE),
    ('grok-composer-2.5-fast', 'grok-composer-2.5-fast', 'Grok Composer 2.5 Fast', 'chat', '["cli_oauth"]', '', '["grok-composer"]', TRUE),
    ('grok-4.20-fast', 'grok-4.20-fast', 'Grok 4.20 Fast', 'chat', '["grok_sso"]', '', '[]', TRUE),
    ('grok-4.20-auto', 'grok-4.20-auto', 'Grok 4.20 Auto', 'chat', '["grok_sso"]', 'super', '[]', TRUE),
    ('grok-4.20-expert', 'grok-4.20-expert', 'Grok 4.20 Expert', 'chat', '["grok_sso"]', 'super', '[]', TRUE),
    ('grok-4.20-heavy', 'grok-4.20-heavy', 'Grok 4.20 Heavy', 'chat', '["grok_sso"]', 'heavy', '[]', TRUE),
    ('grok-4.3-console', 'grok-4.3', 'Grok 4.3 Console', 'chat', '["console_sso"]', '', '[]', TRUE),
    ('grok-4.3-low', 'grok-4.3', 'Grok 4.3 Low', 'chat', '["console_sso"]', '', '[]', TRUE),
    ('grok-4.3-medium', 'grok-4.3', 'Grok 4.3 Medium', 'chat', '["console_sso"]', '', '[]', TRUE),
    ('grok-4.3-high', 'grok-4.3', 'Grok 4.3 High', 'chat', '["console_sso"]', '', '[]', TRUE),
    ('grok-imagine-image-lite', 'grok-imagine-image-lite', 'Grok Imagine Image Lite', 'image', '["grok_sso","cli_oauth"]', '', '[]', TRUE),
    ('grok-imagine-image', 'grok-imagine-image', 'Grok Imagine Image', 'image', '["grok_sso","cli_oauth"]', 'super', '["image-pro","grok-imagine-image-quality"]', TRUE),
    ('grok-imagine-image-edit', 'grok-imagine-image-edit', 'Grok Imagine Image Edit', 'image', '["grok_sso","cli_oauth"]', 'super', '["grok-imagine-edit"]', TRUE),
    ('grok-imagine-video', 'grok-imagine-video', 'Grok Imagine Video', 'video', '["grok_sso","cli_oauth"]', 'super', '["grok-imagine-video-1.5"]', TRUE)
ON CONFLICT (id) DO NOTHING;
