BEGIN;

ALTER TABLE router.model_router_request_telemetry
  DROP COLUMN client_git_dirty,
  DROP COLUMN client_git_branch,
  DROP COLUMN client_git_head_sha;

ALTER TABLE router.model_router_installations
  DROP COLUMN trial_shadow_daily_ceiling_usd_micros,
  DROP COLUMN trial_shadow_sample_rate,
  DROP COLUMN trial_enrollment_id,
  DROP COLUMN trial_capture_enabled;

COMMIT;
