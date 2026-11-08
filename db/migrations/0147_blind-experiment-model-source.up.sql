BEGIN;

ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN blind_experiment_model_source VARCHAR(32),
    ADD COLUMN blind_experiment_treatment_applied BOOLEAN,
    ADD COLUMN blind_experiment_baseline_model VARCHAR(128),
    ADD COLUMN blind_experiment_model_changed BOOLEAN;

COMMIT;
