BEGIN;

DELETE FROM router.model_router_external_api_keys WHERE auth_type = 'anthropic_wif';

ALTER TABLE router.model_router_external_api_keys
    DROP CONSTRAINT model_router_external_api_keys_federation_check,
    DROP COLUMN federation_config,
    DROP CONSTRAINT model_router_external_api_keys_auth_type_check,
    ADD CONSTRAINT model_router_external_api_keys_auth_type_check
        CHECK (auth_type IN ('bearer', 'keypair_jwt', 'wif', 'azure_entra'));

COMMIT;
