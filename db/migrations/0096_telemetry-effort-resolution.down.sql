BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN effort_arm,
    DROP COLUMN effort_selected,
    DROP COLUMN effort_sent,
    DROP COLUMN effort_source;

COMMIT;
