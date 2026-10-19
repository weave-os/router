BEGIN;

ALTER TABLE router.model_router_installations
  ALTER COLUMN hide_terminal_surfaces SET DEFAULT FALSE;

COMMIT;
