BEGIN;

-- Optional per-installation model allowlists selected from the caller's
-- subscription state. Empty arrays preserve the existing no-restriction
-- behavior. The router chooses one at request time from the usage observer:
-- models_when_subscription_active while the subscription has headroom, and
-- models_when_subscription_inactive after usage.Observer reports exhaustion.
ALTER TABLE router.model_router_installations
  ADD COLUMN models_when_subscription_active TEXT[] NOT NULL DEFAULT '{}',
  ADD COLUMN models_when_subscription_inactive TEXT[] NOT NULL DEFAULT '{}';

COMMENT ON COLUMN router.model_router_installations.models_when_subscription_active IS
  'Optional model allowlist while the caller subscription is active; empty means no conditional restriction.';
COMMENT ON COLUMN router.model_router_installations.models_when_subscription_inactive IS
  'Optional model allowlist while the caller subscription is exhausted; empty means no conditional restriction.';

COMMIT;
