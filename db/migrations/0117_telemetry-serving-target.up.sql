BEGIN;

ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN serving_target VARCHAR;

ALTER TABLE router.model_router_request_telemetry
    ADD CONSTRAINT model_router_request_telemetry_serving_target_check
        CHECK (serving_target IS NULL OR serving_target IN ('staging', 'prod/stable', 'prod/weave-internal'));

COMMIT;
