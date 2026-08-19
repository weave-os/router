BEGIN;

-- Positive counterpart to excluded_models: when non-empty, routing is confined
-- to these models. Composes with (does not replace) excluded_models — the
-- effective set is allowed_models minus excluded_models. Empty array means "no
-- restriction", matching excluded_models' empty-means-none convention, so NULL
-- and empty never have to be distinguished at the read site.
ALTER TABLE router.model_router_installations
  ADD COLUMN allowed_models TEXT[] NOT NULL DEFAULT '{}';

COMMENT ON COLUMN router.model_router_installations.allowed_models IS 'Org-level positive model allowlist. Empty = no restriction. Effective set = allowed_models minus excluded_models. Fail-closed: an allowlist with no eligible overlap refuses the turn.';

COMMIT;
