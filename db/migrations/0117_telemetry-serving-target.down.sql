BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP CONSTRAINT model_router_request_telemetry_serving_target_check;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN serving_target;

COMMIT;
