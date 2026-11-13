BEGIN;

-- cache_creation_tokens stays the all-TTL total; cache_creation_1h_tokens is
-- its 1-hour-TTL share. speed and inference_geo are the provider-reported
-- usage.speed / usage.inference_geo, NULL when not reported.
ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN cache_creation_1h_tokens INT,
    ADD COLUMN speed TEXT,
    ADD COLUMN inference_geo TEXT;

COMMIT;
