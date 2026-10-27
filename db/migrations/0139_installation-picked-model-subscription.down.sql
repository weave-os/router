BEGIN;

ALTER TABLE router.model_router_installations
  DROP COLUMN picked_model_subscription_enabled;

COMMIT;
