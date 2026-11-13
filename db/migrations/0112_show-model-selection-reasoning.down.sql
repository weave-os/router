BEGIN;

ALTER TABLE router.model_router_installations
  DROP COLUMN show_model_selection_reasoning;

COMMIT;
