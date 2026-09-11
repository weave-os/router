BEGIN;

-- The provider CHECK is an allowlist, so a TrustedRouter BYOK token is rejected
-- at INSERT without this. Carries forward 0052's allowlist, which is still the live one — 0061, 0064
-- and 0067 touch this table but leave the provider CHECK alone.
ALTER TABLE router.model_router_external_api_keys
  DROP CONSTRAINT model_router_external_api_keys_provider_check;

ALTER TABLE router.model_router_external_api_keys
  ADD CONSTRAINT model_router_external_api_keys_provider_check
  CHECK (provider IN (
    'anthropic','openai','google','openrouter','fireworks',
    'bedrock','makora','together','xai','anthropic_gateway','openai_gateway',
    'trustedrouter'
  ));

COMMIT;
