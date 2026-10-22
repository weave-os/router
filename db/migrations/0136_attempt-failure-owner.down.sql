BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN IF EXISTS failure_owner;

ALTER TABLE router.model_router_inference_attempts
    DROP COLUMN IF EXISTS body_translated,
    DROP COLUMN IF EXISTS failure_owner,
    DROP COLUMN IF EXISTS error_class;

COMMIT;
