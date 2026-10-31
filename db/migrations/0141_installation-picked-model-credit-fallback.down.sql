BEGIN;

ALTER TABLE router.model_router_installations
  DROP COLUMN picked_model_credit_fallback_enabled;

COMMIT;
