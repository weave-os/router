BEGIN;

ALTER TABLE router.model_router_installations
  DROP COLUMN allowed_providers;

COMMIT;
