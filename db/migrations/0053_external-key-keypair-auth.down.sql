BEGIN;

ALTER TABLE router.model_router_external_api_keys
  DROP CONSTRAINT model_router_external_api_keys_keypair_auth_check,
  DROP COLUMN auth_user,
  DROP COLUMN auth_account,
  DROP COLUMN auth_type;

COMMIT;
