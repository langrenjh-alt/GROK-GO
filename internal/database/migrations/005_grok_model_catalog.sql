ALTER TABLE models
    ADD COLUMN IF NOT EXISTS prefer_best BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE models
    ADD COLUMN IF NOT EXISTS catalog_managed BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE models
    DROP CONSTRAINT IF EXISTS models_capability_check;

ALTER TABLE models
    ADD CONSTRAINT models_capability_check
    CHECK (capability IN ('chat', 'responses', 'messages', 'image', 'image_edit', 'video'));

-- Rows that still exactly match the original 002 seed have not been customized.
-- Only those rows enter catalog management and may receive corrected presets.
WITH legacy (
    id, upstream_model, display_name, capability, credential_kinds,
    minimum_tier, aliases, enabled
) AS (VALUES
    ('grok-4.5', 'grok-4.5', 'Grok 4.5', 'chat', '["cli_oauth"]'::jsonb, '', '["grok-latest","grok"]'::jsonb, TRUE),
    ('grok-4.3', 'grok-4.3', 'Grok 4.3', 'chat', '["cli_oauth","console_sso"]'::jsonb, '', '[]'::jsonb, TRUE),
    ('grok-build-0.1', 'grok-build-0.1', 'Grok Build', 'chat', '["cli_oauth"]'::jsonb, '', '["grok-build","grok-build-console"]'::jsonb, TRUE),
    ('grok-composer-2.5-fast', 'grok-composer-2.5-fast', 'Grok Composer 2.5 Fast', 'chat', '["cli_oauth"]'::jsonb, '', '["grok-composer"]'::jsonb, TRUE),
    ('grok-4.20-fast', 'grok-4.20-fast', 'Grok 4.20 Fast', 'chat', '["grok_sso"]'::jsonb, '', '[]'::jsonb, TRUE),
    ('grok-4.20-auto', 'grok-4.20-auto', 'Grok 4.20 Auto', 'chat', '["grok_sso"]'::jsonb, 'super', '[]'::jsonb, TRUE),
    ('grok-4.20-expert', 'grok-4.20-expert', 'Grok 4.20 Expert', 'chat', '["grok_sso"]'::jsonb, 'super', '[]'::jsonb, TRUE),
    ('grok-4.20-heavy', 'grok-4.20-heavy', 'Grok 4.20 Heavy', 'chat', '["grok_sso"]'::jsonb, 'heavy', '[]'::jsonb, TRUE),
    ('grok-4.3-console', 'grok-4.3', 'Grok 4.3 Console', 'chat', '["console_sso"]'::jsonb, '', '[]'::jsonb, TRUE),
    ('grok-4.3-low', 'grok-4.3', 'Grok 4.3 Low', 'chat', '["console_sso"]'::jsonb, '', '[]'::jsonb, TRUE),
    ('grok-4.3-medium', 'grok-4.3', 'Grok 4.3 Medium', 'chat', '["console_sso"]'::jsonb, '', '[]'::jsonb, TRUE),
    ('grok-4.3-high', 'grok-4.3', 'Grok 4.3 High', 'chat', '["console_sso"]'::jsonb, '', '[]'::jsonb, TRUE),
    ('grok-imagine-image-lite', 'grok-imagine-image-lite', 'Grok Imagine Image Lite', 'image', '["grok_sso","cli_oauth"]'::jsonb, '', '[]'::jsonb, TRUE),
    ('grok-imagine-image', 'grok-imagine-image', 'Grok Imagine Image', 'image', '["grok_sso","cli_oauth"]'::jsonb, 'super', '["image-pro","grok-imagine-image-quality"]'::jsonb, TRUE),
    ('grok-imagine-image-edit', 'grok-imagine-image-edit', 'Grok Imagine Image Edit', 'image', '["grok_sso","cli_oauth"]'::jsonb, 'super', '["grok-imagine-edit"]'::jsonb, TRUE),
    ('grok-imagine-video', 'grok-imagine-video', 'Grok Imagine Video', 'video', '["grok_sso","cli_oauth"]'::jsonb, 'super', '["grok-imagine-video-1.5"]'::jsonb, TRUE)
)
UPDATE models AS current
SET catalog_managed = TRUE
FROM legacy
WHERE current.id = legacy.id
  AND current.upstream_model = legacy.upstream_model
  AND current.display_name = legacy.display_name
  AND current.capability = legacy.capability
  AND current.credential_kinds = legacy.credential_kinds
  AND current.minimum_tier = legacy.minimum_tier
  AND current.aliases = legacy.aliases
  AND current.enabled = legacy.enabled;

-- The stable public Grok 4.3 preset owns this legacy internal name as an alias.
DELETE FROM models
WHERE id = 'grok-4.3-console' AND catalog_managed = TRUE;

-- This old CLI-only preset is absent from the current Grok registry.
UPDATE models
SET enabled = FALSE, updated_at = now()
WHERE id = 'grok-composer-2.5-fast' AND catalog_managed = TRUE;

INSERT INTO models (
    id, upstream_model, display_name, capability, credential_kinds,
    minimum_tier, aliases, prefer_best, catalog_managed, enabled
) VALUES
    ('grok-4.20-0309-non-reasoning', 'fast', 'Grok 4.20 0309 Non-Reasoning', 'chat', '["grok_sso"]'::jsonb, '', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.20-0309', 'auto', 'Grok 4.20 0309', 'chat', '["grok_sso"]'::jsonb, 'super', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.20-0309-reasoning', 'expert', 'Grok 4.20 0309 Reasoning', 'chat', '["grok_sso"]'::jsonb, 'super', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.20-0309-non-reasoning-super', 'fast', 'Grok 4.20 0309 Non-Reasoning Super', 'chat', '["grok_sso"]'::jsonb, 'super', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.20-0309-super', 'auto', 'Grok 4.20 0309 Super', 'chat', '["grok_sso"]'::jsonb, 'super', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.20-0309-reasoning-super', 'expert', 'Grok 4.20 0309 Reasoning Super', 'chat', '["grok_sso"]'::jsonb, 'super', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.20-0309-non-reasoning-heavy', 'fast', 'Grok 4.20 0309 Non-Reasoning Heavy', 'chat', '["grok_sso"]'::jsonb, 'heavy', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.20-0309-heavy', 'auto', 'Grok 4.20 0309 Heavy', 'chat', '["grok_sso"]'::jsonb, 'heavy', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.20-0309-reasoning-heavy', 'expert', 'Grok 4.20 0309 Reasoning Heavy', 'chat', '["grok_sso"]'::jsonb, 'heavy', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.20-multi-agent-0309', 'heavy', 'Grok 4.20 Multi-Agent 0309', 'chat', '["grok_sso"]'::jsonb, 'heavy', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.20-fast', 'fast', 'Grok 4.20 Fast', 'chat', '["grok_sso"]'::jsonb, '', '[]'::jsonb, TRUE, TRUE, TRUE),
    ('grok-4.3-fast', 'fast', 'Grok 4.3 Fast', 'chat', '["grok_sso"]'::jsonb, '', '[]'::jsonb, TRUE, TRUE, TRUE),
    ('grok-4.20-auto', 'auto', 'Grok 4.20 Auto', 'chat', '["grok_sso"]'::jsonb, 'super', '[]'::jsonb, TRUE, TRUE, TRUE),
    ('grok-4.20-expert', 'expert', 'Grok 4.20 Expert', 'chat', '["grok_sso"]'::jsonb, 'super', '[]'::jsonb, TRUE, TRUE, TRUE),
    ('grok-4.20-heavy', 'heavy', 'Grok 4.20 Heavy', 'chat', '["grok_sso"]'::jsonb, 'heavy', '[]'::jsonb, TRUE, TRUE, TRUE),
    ('grok-4.3-beta', 'grok-420-computer-use-sa', 'Grok 4.3 Beta', 'chat', '["grok_sso"]'::jsonb, 'super', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-imagine-image-lite', 'fast', 'Grok Imagine Image Lite', 'image', '["grok_sso"]'::jsonb, '', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-imagine-image', 'auto', 'Grok Imagine Image', 'image', '["grok_sso"]'::jsonb, 'super', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-imagine-image-pro', 'auto', 'Grok Imagine Image Pro', 'image', '["grok_sso"]'::jsonb, 'super', '["image-pro","grok-imagine-image-quality"]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-imagine-image-edit', 'auto', 'Grok Imagine Image Edit', 'image_edit', '["grok_sso"]'::jsonb, 'super', '["grok-imagine-edit"]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-imagine-video', 'auto', 'Grok Imagine Video', 'video', '["grok_sso"]'::jsonb, 'super', '["grok-imagine-video-1.5"]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.5', 'grok-4.5', 'Grok 4.5', 'chat', '["cli_oauth"]'::jsonb, '', '["grok-4.5-cli","grok-latest","grok"]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.3', 'grok-4.3', 'Grok 4.3 (Console)', 'chat', '["console_sso"]'::jsonb, '', '["grok-4.3-console"]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.3-low', 'grok-4.3', 'Grok 4.3 Low Thinking', 'chat', '["console_sso"]'::jsonb, '', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.3-medium', 'grok-4.3', 'Grok 4.3 Medium Thinking', 'chat', '["console_sso"]'::jsonb, '', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.3-high', 'grok-4.3', 'Grok 4.3 High Thinking', 'chat', '["console_sso"]'::jsonb, '', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.20-0309-reasoning-console', 'grok-4.20-0309-reasoning', 'Grok 4.20 0309 Reasoning (Console)', 'chat', '["console_sso"]'::jsonb, '', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.20-0309-console', 'grok-4.20-0309', 'Grok 4.20 0309 (Console)', 'chat', '["console_sso"]'::jsonb, '', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.20-multi-agent-console', 'grok-4.20-multi-agent-0309', 'Grok 4.20 Multi-Agent (Console)', 'chat', '["console_sso"]'::jsonb, '', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.20-multi-agent-low', 'grok-4.20-multi-agent-0309', 'Grok 4.20 Multi-Agent Low', 'chat', '["console_sso"]'::jsonb, '', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.20-multi-agent-medium', 'grok-4.20-multi-agent-0309', 'Grok 4.20 Multi-Agent Medium', 'chat', '["console_sso"]'::jsonb, '', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.20-multi-agent-high', 'grok-4.20-multi-agent-0309', 'Grok 4.20 Multi-Agent High', 'chat', '["console_sso"]'::jsonb, '', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.20-multi-agent-xhigh', 'grok-4.20-multi-agent-0309', 'Grok 4.20 Multi-Agent XHigh', 'chat', '["console_sso"]'::jsonb, '', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-4.20-0309-non-reasoning-console', 'grok-4.20-0309-non-reasoning', 'Grok 4.20 0309 Non-Reasoning (Console)', 'chat', '["console_sso"]'::jsonb, '', '[]'::jsonb, FALSE, TRUE, TRUE),
    ('grok-build-0.1', 'grok-build-0.1', 'Grok Build 0.1 (Console)', 'chat', '["console_sso"]'::jsonb, '', '["grok-build-console","grok-build"]'::jsonb, FALSE, TRUE, TRUE)
ON CONFLICT (id) DO UPDATE SET
    upstream_model = EXCLUDED.upstream_model,
    display_name = EXCLUDED.display_name,
    capability = EXCLUDED.capability,
    credential_kinds = EXCLUDED.credential_kinds,
    minimum_tier = EXCLUDED.minimum_tier,
    aliases = EXCLUDED.aliases,
    prefer_best = EXCLUDED.prefer_best,
    catalog_managed = TRUE,
    enabled = EXCLUDED.enabled,
    updated_at = now()
WHERE models.catalog_managed = TRUE;
