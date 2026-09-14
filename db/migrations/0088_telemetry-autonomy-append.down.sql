BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN autonomy_append_fired;

COMMIT;
