BEGIN;

ALTER TABLE router.model_router_installations
  DROP COLUMN fast_mode_models;

COMMIT;
