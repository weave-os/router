BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN requested_allowed_models;

COMMIT;
