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

-- Client-reported git context parsed from the Claude Code system prompt's
-- gitStatus block on the first turn of a session, for installations in trial
-- mode only. Tells the trial benchmark which tree a session started from when
-- the WorkWeave plugin (the primary structured source) is absent.
-- client_git_head_sha is stored as the client abbreviates it; WorkWeave
-- compares it by prefix against the plugin's full sha. All three are NULL
-- when the block was absent, unparseable, the turn was not a session's first,
-- or the installation is not in trial mode. Never read on the routing path.
ALTER TABLE router.model_router_request_telemetry
  ADD COLUMN client_git_head_sha VARCHAR(40),
  ADD COLUMN client_git_branch VARCHAR(255),
  ADD COLUMN client_git_dirty BOOLEAN;

COMMIT;
