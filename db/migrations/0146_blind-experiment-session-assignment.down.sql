BEGIN;

DROP TABLE IF EXISTS router.blind_experiment_session_assignments;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN IF EXISTS blind_experiment_session_key,
    DROP COLUMN IF EXISTS blind_experiment_mode;

ALTER TABLE router.blind_router_experiment_configurations
    DROP COLUMN IF EXISTS session_mode;

COMMIT;
