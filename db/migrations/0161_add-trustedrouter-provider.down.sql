BEGIN;

-- Restore the 0052 allowlist.
DELETE FROM router.model_router_external_api_keys WHERE provider = 'trustedrouter';

ALTER TABLE router.model_router_external_api_keys
  DROP CONSTRAINT model_router_external_api_keys_provider_check;

ALTER TABLE router.model_router_external_api_keys
  ADD CONSTRAINT model_router_external_api_keys_provider_check
  CHECK (provider IN (
    'anthropic','openai','google','openrouter','fireworks',
    'bedrock','makora','together','xai','anthropic_gateway','openai_gateway'
  ));

COMMIT;
