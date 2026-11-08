BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN IF EXISTS blind_experiment_model_changed,
    DROP COLUMN IF EXISTS blind_experiment_baseline_model,
    DROP COLUMN IF EXISTS blind_experiment_treatment_applied,
    DROP COLUMN IF EXISTS blind_experiment_model_source;

COMMIT;
