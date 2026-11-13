BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN codex_limit_headers;

COMMIT;
