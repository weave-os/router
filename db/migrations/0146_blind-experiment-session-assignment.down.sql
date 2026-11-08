BEGIN;

DROP TABLE router.blind_experiment_session_assignments;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN blind_experiment_session_key,
    DROP COLUMN blind_experiment_mode;

ALTER TABLE router.blind_router_experiment_configurations
    DROP COLUMN session_mode;

COMMIT;
