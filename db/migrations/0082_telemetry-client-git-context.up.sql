BEGIN;

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
  ADD COLUMN client_git_branch VARCHAR,
  ADD COLUMN client_git_dirty BOOLEAN;

COMMIT;
