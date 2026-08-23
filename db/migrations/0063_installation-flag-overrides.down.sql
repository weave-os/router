BEGIN;

DROP TABLE router.flag_definitions;

ALTER TABLE router.model_router_installations
  DROP COLUMN flag_overrides;

COMMIT;
