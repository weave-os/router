BEGIN;

-- New installations hide terminal surfaces by default; existing rows keep their saved setting.
ALTER TABLE router.model_router_installations
  ALTER COLUMN hide_terminal_surfaces SET DEFAULT TRUE;

COMMIT;
