BEGIN;

-- The narrower constraint includes deleted rows, so remove federation rows too.
DELETE FROM router.model_router_external_api_keys WHERE auth_type = 'anthropic_wif';

ALTER TABLE router.model_router_external_api_keys
  DROP CONSTRAINT model_router_external_api_keys_anthropic_wif_check,
  DROP CONSTRAINT model_router_external_api_keys_auth_type_check,
  ADD CONSTRAINT model_router_external_api_keys_auth_type_check
    CHECK (auth_type IN ('bearer', 'keypair_jwt', 'wif', 'azure_entra')),
  DROP COLUMN anthropic_federation_rule_id,
  DROP COLUMN anthropic_organization_id,
  DROP COLUMN anthropic_service_account_id,
  DROP COLUMN anthropic_workspace_id;

COMMIT;
