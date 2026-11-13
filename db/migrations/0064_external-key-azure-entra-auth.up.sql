BEGIN;

ALTER TABLE router.model_router_external_api_keys
  DROP CONSTRAINT model_router_external_api_keys_auth_type_check,
  ADD CONSTRAINT model_router_external_api_keys_auth_type_check
    CHECK (auth_type IN ('bearer', 'keypair_jwt', 'wif', 'azure_entra')),
  ADD CONSTRAINT model_router_external_api_keys_azure_entra_auth_check
    CHECK (auth_type <> 'azure_entra' OR (auth_account IS NOT NULL AND auth_user IS NOT NULL));

COMMENT ON COLUMN router.model_router_external_api_keys.auth_type IS 'bearer = send the stored secret as-is, keypair_jwt = the secret is an RSA private key the router signs short-lived JWTs with, wif = no stored secret and the router attests its own workload identity, azure_entra = the secret is an Azure Entra client secret used to mint a short-lived bearer token';
COMMENT ON COLUMN router.model_router_external_api_keys.auth_account IS 'Account identifier for keypair_jwt, or Microsoft Entra tenant ID for azure_entra';
COMMENT ON COLUMN router.model_router_external_api_keys.auth_user IS 'User/principal for keypair_jwt, or Microsoft Entra client ID for azure_entra';

COMMIT;
