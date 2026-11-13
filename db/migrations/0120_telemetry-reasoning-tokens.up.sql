BEGIN;

ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN reasoning_tokens INT;

COMMIT;
