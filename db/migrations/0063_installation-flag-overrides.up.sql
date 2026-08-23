BEGIN;

-- Sparse per-organization overrides for the router's behavioral feature flags.
-- Each flag's deployment-wide default stays in its ROUTER_* env var, resolved at
-- boot; this column layers a per-installation override on top of it, so a flag
-- can be piloted for one organization without a global rollout. Absent key means
-- "inherit the deployment default", which is why the override set is sparse
-- rather than a fully-populated snapshot: a snapshot would silently freeze an
-- org's behavior against later default changes.
--
-- JSONB on the existing installation row rather than a column per flag: this row
-- is already loaded on the hot auth path (see
-- GetActiveModelRouterAPIKeyWithInstallationByHash) behind the API-key LRU, so
-- overrides cost no extra query, no extra cache, and no added request latency.
-- Keys are validated against the compiled-in registry in internal/flags on both
-- read and write; unknown or wrongly-typed keys are rejected, never coerced.
ALTER TABLE router.model_router_installations
  ADD COLUMN flag_overrides JSONB NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN router.model_router_installations.flag_overrides IS 'Sparse per-org behavioral flag overrides, keyed by internal/flags registry key. Empty object = inherit every deployment default. Precedence: header override > this > env default, unless ROUTER_FLAG_OVERRIDES_DISABLED is set.';

-- Published mirror of the router's compiled-in flag registry. The router upserts
-- every row at boot from internal/flags.Registry, stamping the deployment default
-- it actually resolved from the environment. It exists so the Weave control plane
-- can render and validate the override admin UI without importing router Go code
-- or calling the router over HTTP -- it already reads this schema directly.
--
-- The router treats this table as write-only output; nothing in the request path
-- reads it, so a stale or missing row degrades the admin UI, never routing.
CREATE TABLE router.flag_definitions (
    key                TEXT PRIMARY KEY,
    kind               TEXT NOT NULL,
    env_var            TEXT NOT NULL,
    deployment_default TEXT,
    org_overridable    BOOLEAN NOT NULL,
    description        TEXT NOT NULL DEFAULT '',
    registry_version   INTEGER NOT NULL,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE router.flag_definitions IS 'Published mirror of internal/flags.Registry, upserted by the router at boot. Read by the Weave control plane to render the per-org flag override admin UI. Never read on the request path.';
COMMENT ON COLUMN router.flag_definitions.kind IS 'Value type: bool, int, float, or string. A stored override whose JSON type disagrees is rejected at parse time.';
COMMENT ON COLUMN router.flag_definitions.deployment_default IS 'Deployment default as resolved from env_var at the last boot, rendered as text. Display only; the routing path reads the live in-process value, not this column.';
COMMENT ON COLUMN router.flag_definitions.org_overridable IS 'Whether a per-organization override may be written for this flag. A registered-but-not-overridable flag is shown read-only and rejects writes.';
COMMENT ON COLUMN router.flag_definitions.registry_version IS 'Monotonic internal/flags.Registry version. Pruning only considers rows at or below the current version, so an older rolling-deploy revision cannot delete a newer definition.';

COMMIT;
