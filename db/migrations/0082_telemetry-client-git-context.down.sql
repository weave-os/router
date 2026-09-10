BEGIN;

ALTER TABLE router.model_router_request_telemetry
  DROP COLUMN client_git_dirty,
  DROP COLUMN client_git_branch,
  DROP COLUMN client_git_head_sha;

COMMIT;
