BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP CONSTRAINT model_router_request_telemetry_blind_experiment_assignment_source,
    DROP CONSTRAINT model_router_request_telemetry_blind_experiment_arm,
    DROP COLUMN blind_experiment_subject_key,
    DROP COLUMN blind_experiment_assignment_source,
    DROP COLUMN blind_experiment_arm;

DROP TABLE router.blind_router_experiment_assignments;
DROP TABLE router.blind_router_experiment_subject_overrides;

ALTER TABLE router.model_router_users
    DROP CONSTRAINT model_router_users_id_installation_id_unique;

DROP TABLE router.blind_router_experiment_configurations;

COMMIT;
