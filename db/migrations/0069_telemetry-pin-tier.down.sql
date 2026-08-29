BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN pin_tier;

COMMIT;
