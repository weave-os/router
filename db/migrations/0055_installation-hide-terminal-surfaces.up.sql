BEGIN;

-- Per-installation toggle to hide the router's terminal surfaces from
-- engineers: the "✦ Weave Router → <model>" routing marker, the trailing
-- "/rf" feedback hint, and the router statusline. When true, requests route
-- identically but nothing routing-related is rendered in the caller's
-- terminal. Feedback submission/recording (/rf, signed rating links) is
-- unaffected. Default false preserves today's behavior for every existing
-- installation.
ALTER TABLE router.model_router_installations
  ADD COLUMN hide_terminal_surfaces BOOLEAN NOT NULL DEFAULT FALSE;

COMMIT;
