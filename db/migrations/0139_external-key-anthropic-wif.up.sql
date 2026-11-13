BEGIN;

ALTER TABLE router.model_router_external_api_keys
    DROP CONSTRAINT model_router_external_api_keys_auth_type_check,
    ADD CONSTRAINT model_router_external_api_keys_auth_type_check
        CHECK (auth_type IN ('bearer', 'keypair_jwt', 'wif', 'azure_entra', 'anthropic_wif')),
    ADD COLUMN federation_config JSONB,
    ADD CONSTRAINT model_router_external_api_keys_federation_check CHECK (
        (auth_type <> 'anthropic_wif' AND federation_config IS NULL) OR
        (auth_type = 'anthropic_wif' AND provider = 'anthropic'
            AND auth_account IS NULL AND auth_user IS NULL
            AND octet_length(key_ciphertext) = 0
            AND (base_url IS NULL OR base_url = 'https://api.anthropic.com')
            AND federation_config IS NOT NULL
            AND jsonb_typeof(federation_config) = 'object'
            AND federation_config ?& ARRAY['federation_rule_id', 'organization_id', 'service_account_id', 'audience'])
    );

COMMENT ON COLUMN router.model_router_external_api_keys.federation_config IS
    'Non-secret Anthropic federation rule, organization, service account, workspace and audience identifiers';

COMMIT;
