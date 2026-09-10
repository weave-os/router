BEGIN;

-- Consented trial-period benchmark enrollment, mirrored per installation.
-- WorkWeave owns these values and writes them with raw SQL against the shared
-- router schema (the ai_training_allowed pattern); the router only reads them.
-- trial_capture_enabled gates the trial-only telemetry capture (client
-- git-context parse on a session's first turn). trial_enrollment_id is the
-- WorkWeave router_trial_enrollments.id the installation is enrolled under.
-- trial_shadow_sample_rate / trial_shadow_daily_ceiling_usd_micros bound the
-- trial's shadow-evaluation spend; nothing in the router reads them yet.
ALTER TABLE router.model_router_installations
  ADD COLUMN trial_capture_enabled BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN trial_enrollment_id UUID,
  ADD COLUMN trial_shadow_sample_rate NUMERIC(5,4),
  ADD COLUMN trial_shadow_daily_ceiling_usd_micros BIGINT;

COMMIT;
