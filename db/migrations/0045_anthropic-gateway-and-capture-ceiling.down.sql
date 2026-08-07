BEGIN;

ALTER TABLE router.model_router_installations
  DROP COLUMN content_capture_mode;

-- Narrowing the CHECK fails while gateway rows exist. Destructive by
-- necessity: the rolled-back schema has no legal representation for them.
DELETE FROM router.model_router_external_api_keys
WHERE provider = 'anthropic_gateway';

ALTER TABLE router.model_router_external_api_keys
  DROP CONSTRAINT model_router_external_api_keys_provider_check;

ALTER TABLE router.model_router_external_api_keys
  ADD CONSTRAINT model_router_external_api_keys_provider_check
  CHECK (provider IN (
    'anthropic','openai','google','openrouter','fireworks',
    'bedrock','makora','together','xai'
  ));

COMMIT;
