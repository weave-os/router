BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN blind_experiment_baseline_source;

COMMIT;
