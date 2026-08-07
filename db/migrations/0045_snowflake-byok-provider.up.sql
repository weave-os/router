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

COMMIT;
