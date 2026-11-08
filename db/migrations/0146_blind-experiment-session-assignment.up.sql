BEGIN;

ALTER TABLE router.blind_router_experiment_configurations
    ADD COLUMN session_mode BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN blind_experiment_mode VARCHAR(16),
    ADD COLUMN blind_experiment_session_key VARCHAR(128);

COMMIT;
