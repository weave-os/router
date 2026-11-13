BEGIN;

-- Some gateway tenants forbid long-lived tokens and authenticate with a key
-- pair instead: the stored secret is an RSA private key, and the router mints a
-- short-lived signed token per request window. The account and user identify
-- the signing principal to the upstream. 'bearer' keeps today's behavior — the
-- stored secret is sent verbatim.
ALTER TABLE router.model_router_external_api_keys
  ADD COLUMN auth_type TEXT NOT NULL DEFAULT 'bearer'
    CHECK (auth_type IN ('bearer', 'keypair_jwt')),
  ADD COLUMN auth_account TEXT,
  ADD COLUMN auth_user TEXT,
  ADD CONSTRAINT model_router_external_api_keys_keypair_auth_check
    CHECK (auth_type <> 'keypair_jwt' OR (auth_account IS NOT NULL AND auth_user IS NOT NULL));

COMMENT ON COLUMN router.model_router_external_api_keys.auth_type IS 'bearer = send the stored secret as-is, keypair_jwt = the secret is an RSA private key the router signs short-lived JWTs with';
COMMENT ON COLUMN router.model_router_external_api_keys.auth_account IS 'Account identifier the minted JWT is issued for; NULL unless auth_type is keypair_jwt';
COMMENT ON COLUMN router.model_router_external_api_keys.auth_user IS 'User the minted JWT authenticates as; NULL unless auth_type is keypair_jwt';

COMMIT;
