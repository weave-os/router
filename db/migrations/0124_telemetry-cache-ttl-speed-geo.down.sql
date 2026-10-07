BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN cache_creation_1h_tokens,
    DROP COLUMN speed,
    DROP COLUMN inference_geo;

COMMIT;
