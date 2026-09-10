BEGIN;

ALTER TABLE router.model_router_installations
  DROP COLUMN trial_shadow_daily_ceiling_usd_micros,
  DROP COLUMN trial_shadow_sample_rate,
  DROP COLUMN trial_enrollment_id,
  DROP COLUMN trial_capture_enabled;

COMMIT;
