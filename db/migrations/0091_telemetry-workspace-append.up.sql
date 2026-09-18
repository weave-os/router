BEGIN;

-- Observation-only: whether the router appended its workspace-inspection
-- instruction to this request's system prompt (flag cc_workspace_system_append).
-- Nothing on the request path reads this column.
ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN workspace_append_fired BOOLEAN;

COMMENT ON COLUMN router.model_router_request_telemetry.workspace_append_fired IS
    'TRUE when the served attempt carried the workspace-inspection instruction appended by the router (cross-vendor emitters only). NULL when it did not.';

COMMIT;
