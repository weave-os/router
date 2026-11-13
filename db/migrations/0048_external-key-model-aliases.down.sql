BEGIN;

ALTER TABLE router.model_router_external_api_keys
  DROP COLUMN model_aliases;

COMMIT;
