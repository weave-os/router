BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN reasoning_tokens;

COMMIT;
