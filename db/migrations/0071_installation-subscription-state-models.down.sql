BEGIN;

ALTER TABLE router.model_router_installations
  DROP COLUMN models_when_subscription_active,
  DROP COLUMN models_when_subscription_inactive;

COMMIT;
