BEGIN;

ALTER TABLE router.blind_router_experiment_configurations
    ADD COLUMN session_mode BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN blind_experiment_mode VARCHAR(16)
        CHECK (blind_experiment_mode IN ('user', 'cohort', 'session')),
    ADD COLUMN blind_experiment_session_key VARCHAR(128);

CREATE TABLE router.blind_experiment_session_assignments (
    installation_id UUID NOT NULL
        REFERENCES router.blind_router_experiment_configurations (installation_id) ON DELETE CASCADE,
    session_key     VARCHAR(128) NOT NULL,
    arm             VARCHAR(32) NOT NULL CHECK (arm IN ('router_on', 'passthrough')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (installation_id, session_key)
);

COMMIT;
