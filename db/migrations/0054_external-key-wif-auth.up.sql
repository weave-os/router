BEGIN;

-- Workload identity federation: the router authenticates to the upstream with
-- an attestation of its own cloud identity, so there is no tenant secret to
-- store at all. The stored ciphertext is empty and the principal lives in the
-- attestation rather than in auth_account / auth_user.
ALTER TABLE router.model_router_external_api_keys
  DROP CONSTRAINT model_router_external_api_keys_auth_type_check,
  ADD CONSTRAINT model_router_external_api_keys_auth_type_check
    CHECK (auth_type IN ('bearer', 'keypair_jwt', 'wif')),
  ADD CONSTRAINT model_router_external_api_keys_wif_auth_check
    CHECK (auth_type <> 'wif' OR (auth_account IS NULL AND auth_user IS NULL));

COMMENT ON COLUMN router.model_router_external_api_keys.auth_type IS 'bearer = send the stored secret as-is, keypair_jwt = the secret is an RSA private key the router signs short-lived JWTs with, wif = no stored secret, the router attests its own workload identity';

COMMIT;
