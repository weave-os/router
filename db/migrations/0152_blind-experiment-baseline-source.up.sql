BEGIN;

ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN blind_experiment_baseline_source VARCHAR(32);

COMMIT;
