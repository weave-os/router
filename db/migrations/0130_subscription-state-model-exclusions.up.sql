BEGIN;

-- Subscription-state model sets are stored as exclusions from the routable
-- catalog so newly deployed models join both states by default. The positive
-- models_when_subscription_* lists are no longer read and are dropped later.
ALTER TABLE router.model_router_installations
  ADD COLUMN subscription_state_models_enabled BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN models_excluded_when_subscription_active TEXT[] NOT NULL DEFAULT '{}',
  ADD COLUMN models_excluded_when_subscription_inactive TEXT[] NOT NULL DEFAULT '{}';

UPDATE router.model_router_installations
SET subscription_state_models_enabled = true
WHERE cardinality(models_when_subscription_active) > 0
   OR cardinality(models_when_subscription_inactive) > 0;

COMMIT;
