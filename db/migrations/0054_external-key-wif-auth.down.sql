BEGIN;

-- Hard delete, not soft: the narrowed CHECK is validated against every row, so
-- a soft-deleted 'wif' key would still block the rollback.
DELETE FROM router.model_router_external_api_keys WHERE auth_type = 'wif';

ALTER TABLE router.model_router_external_api_keys
  DROP CONSTRAINT model_router_external_api_keys_wif_auth_check,
  DROP CONSTRAINT model_router_external_api_keys_auth_type_check,
  ADD CONSTRAINT model_router_external_api_keys_auth_type_check
    CHECK (auth_type IN ('bearer', 'keypair_jwt'));

COMMENT ON COLUMN router.model_router_external_api_keys.auth_type IS 'bearer = send the stored secret as-is, keypair_jwt = the secret is an RSA private key the router signs short-lived JWTs with';

COMMIT;
