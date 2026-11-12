BEGIN;

ALTER TABLE router.model_router_installations
  DROP COLUMN weave_credit_inference_enabled;

COMMIT;
