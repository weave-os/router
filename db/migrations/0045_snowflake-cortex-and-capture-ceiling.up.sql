BEGIN;

-- Snowflake Cortex BYOK. The provider CHECK is an allowlist, so without this a
-- customer storing their Snowflake PAT is rejected at INSERT — the only way to
-- reach Cortex, whose base URL is per-account and has no deployment default.
ALTER TABLE router.model_router_external_api_keys
  DROP CONSTRAINT model_router_external_api_keys_provider_check;

ALTER TABLE router.model_router_external_api_keys
  ADD CONSTRAINT model_router_external_api_keys_provider_check
  CHECK (provider IN (
    'anthropic','openai','google','openrouter','fireworks',
    'bedrock','makora','together','xai','snowflake'
  ));

-- Per-installation ceiling on prompt/response capture. WV_CAPTURE_CONTENT is
-- deployment-wide, so a tenant with a zero-retention requirement previously had
-- no way to opt out short of a dedicated deploy. NULL means "no override" and
-- keeps today's behavior; a set value can only tighten the deployment mode,
-- never loosen it (see proxy.Service.effectiveCaptureMode).
ALTER TABLE router.model_router_installations
  ADD COLUMN content_capture_mode TEXT
  CHECK (content_capture_mode IN ('off','hashed','full'));

COMMENT ON COLUMN router.model_router_installations.content_capture_mode IS 'Per-installation capture ceiling (off|hashed|full); NULL uses the deployment default';

COMMIT;
