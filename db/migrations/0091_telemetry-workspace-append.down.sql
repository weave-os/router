BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN workspace_append_fired;

COMMIT;
