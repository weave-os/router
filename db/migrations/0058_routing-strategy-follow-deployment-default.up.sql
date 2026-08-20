BEGIN;

-- Realign routing_strategy with the shape 0034 declares. 0034 originally shipped
-- this column as NOT NULL DEFAULT 'cluster' and was edited in place an hour later
-- to be nullable; environments that had already applied the original ran the
-- stricter form and never picked up the edit. The result is live drift: an
-- environment on the pre-edit form silently stamps every new installation
-- 'cluster', which pins it to the legacy scorer instead of following
-- ROUTER_DEFAULT_STRATEGY. Idempotent by construction — on an environment that
-- applied the edited 0034 both statements are already-satisfied no-ops.
ALTER TABLE router.model_router_installations
  ALTER COLUMN routing_strategy DROP DEFAULT,
  ALTER COLUMN routing_strategy DROP NOT NULL;

-- Release the installations the column default captured. Scoped to rows that
-- carry no other policy-rollout state: UpdateInstallationPolicy always co-writes
-- routing_rollout_id / policy_shadow_strategy / policy_debug_enabled /
-- policy_header_overrides_enabled / policy_routing_intent, so a row at defaults
-- across all five was never touched by a deliberate pin and only holds 'cluster'
-- because Postgres filled it in. A genuine operator pin keeps its value.
UPDATE router.model_router_installations
SET routing_strategy = NULL
WHERE routing_strategy = 'cluster'
  AND routing_rollout_id IS NULL
  AND policy_shadow_strategy IS NULL
  AND policy_routing_intent IS NULL
  AND policy_debug_enabled = FALSE
  AND policy_header_overrides_enabled = FALSE;

COMMIT;
