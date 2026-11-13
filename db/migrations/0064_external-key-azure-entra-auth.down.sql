BEGIN;

DELETE FROM router.model_router_external_api_keys
WHERE auth_type = 'azure_entra';

ALTER TABLE router.model_router_external_api_keys
  DROP CONSTRAINT model_router_external_api_keys_azure_entra_auth_check,
  DROP CONSTRAINT model_router_external_api_keys_auth_type_check,
  ADD CONSTRAINT model_router_external_api_keys_auth_type_check
    CHECK (auth_type IN ('bearer', 'keypair_jwt', 'wif'));

COMMENT ON COLUMN router.model_router_external_api_keys.auth_type IS 'bearer = send the stored secret as-is, keypair_jwt = the secret is an RSA private key the router signs short-lived JWTs with, wif = no stored secret and the router attests its own workload identity';
COMMENT ON COLUMN router.model_router_external_api_keys.auth_account IS 'Account identifier the minted JWT is issued for; NULL unless auth_type is keypair_jwt';
COMMENT ON COLUMN router.model_router_external_api_keys.auth_user IS 'User the minted JWT authenticates as; NULL unless auth_type is keypair_jwt';

COMMIT;
