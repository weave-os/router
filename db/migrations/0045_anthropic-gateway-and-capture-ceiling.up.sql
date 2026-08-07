BEGIN;

-- The provider CHECK is an allowlist, so a gateway BYOK token is rejected at
-- INSERT without this.
ALTER TABLE router.model_router_external_api_keys
  DROP CONSTRAINT model_router_external_api_keys_provider_check;

ALTER TABLE router.model_router_external_api_keys
  ADD CONSTRAINT model_router_external_api_keys_provider_check
  CHECK (provider IN (
    'anthropic','openai','google','openrouter','fireworks',
    'bedrock','makora','together','xai','anthropic_gateway'
  ));

-- Per-installation ceiling on the deployment-wide WV_CAPTURE_CONTENT: NULL
-- keeps today's behavior, and a set value can only tighten, never loosen (see
-- proxy.Service.effectiveCaptureMode).
ALTER TABLE router.model_router_installations
  ADD COLUMN content_capture_mode TEXT
  CHECK (content_capture_mode IN ('off','hashed','full'));

COMMENT ON COLUMN router.model_router_installations.content_capture_mode IS 'Per-installation capture ceiling (off|hashed|full); NULL uses the deployment default';

COMMIT;
