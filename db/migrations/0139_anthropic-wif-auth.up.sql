BEGIN;

ALTER TABLE router.model_router_external_api_keys
  ADD COLUMN anthropic_federation_rule_id TEXT,
  ADD COLUMN anthropic_organization_id TEXT,
  ADD COLUMN anthropic_service_account_id TEXT,
  ADD COLUMN anthropic_workspace_id TEXT,
  DROP CONSTRAINT model_router_external_api_keys_auth_type_check,
  ADD CONSTRAINT model_router_external_api_keys_auth_type_check
    CHECK (auth_type IN ('bearer', 'keypair_jwt', 'wif', 'azure_entra', 'anthropic_wif')),
  ADD CONSTRAINT model_router_external_api_keys_anthropic_wif_check CHECK (
    CASE WHEN auth_type = 'anthropic_wif' THEN
      provider = 'anthropic'
      AND NULLIF(BTRIM(anthropic_federation_rule_id), '') IS NOT NULL
      AND NULLIF(BTRIM(anthropic_organization_id), '') IS NOT NULL
      AND NULLIF(BTRIM(anthropic_service_account_id), '') IS NOT NULL
      AND auth_account IS NULL AND auth_user IS NULL AND base_url IS NULL
      AND octet_length(key_ciphertext) = 0
    ELSE
      anthropic_federation_rule_id IS NULL AND anthropic_organization_id IS NULL
      AND anthropic_service_account_id IS NULL AND anthropic_workspace_id IS NULL
    END
  );

COMMIT;
