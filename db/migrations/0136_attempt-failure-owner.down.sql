BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN failure_owner;

ALTER TABLE router.model_router_inference_attempts
    DROP COLUMN body_translated,
    DROP COLUMN failure_owner,
    DROP COLUMN error_class;

COMMIT;
