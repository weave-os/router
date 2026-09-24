BEGIN;

ALTER TABLE router.model_router_installations
  ADD COLUMN show_model_selection_reasoning BOOLEAN NOT NULL DEFAULT FALSE;

COMMIT;
