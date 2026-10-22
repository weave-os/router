BEGIN;

ALTER TABLE router.model_router_installations
  DROP COLUMN models_excluded_when_subscription_inactive,
  DROP COLUMN models_excluded_when_subscription_active,
  DROP COLUMN subscription_state_models_enabled;

COMMIT;
