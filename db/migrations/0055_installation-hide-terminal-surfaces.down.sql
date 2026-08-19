BEGIN;

ALTER TABLE router.model_router_installations
  DROP COLUMN hide_terminal_surfaces;

COMMIT;
