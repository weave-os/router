BEGIN;

CREATE TABLE router.blind_router_experiment_configurations (
    installation_id      UUID PRIMARY KEY
        REFERENCES router.model_router_installations (id) ON DELETE CASCADE,
    organization_id      VARCHAR(36) NOT NULL,
    enabled              BOOLEAN NOT NULL DEFAULT FALSE,
    router_on_percentage SMALLINT NOT NULL,
    seed                 UUID NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT blind_router_experiment_configurations_percentage_range
        CHECK (router_on_percentage BETWEEN 0 AND 100)
);

CREATE INDEX blind_router_experiment_configurations_organization_id_idx
    ON router.blind_router_experiment_configurations (organization_id);

ALTER TABLE router.model_router_users
    ADD CONSTRAINT model_router_users_id_installation_id_unique
    UNIQUE (id, installation_id);

CREATE TABLE router.blind_router_experiment_subject_overrides (
    installation_id       UUID NOT NULL
        REFERENCES router.model_router_installations (id) ON DELETE CASCADE,
    organization_id       VARCHAR(36) NOT NULL,
    canonical_subject_key VARCHAR(128) NOT NULL,
    manual_override       VARCHAR(32) NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (installation_id, canonical_subject_key),
    CONSTRAINT blind_router_experiment_subject_overrides_arm
        CHECK (manual_override IN ('router_on', 'passthrough'))
);

CREATE INDEX blind_router_experiment_subject_overrides_organization_id_idx
    ON router.blind_router_experiment_subject_overrides (organization_id);

CREATE TABLE router.blind_router_experiment_assignments (
    router_user_id         UUID PRIMARY KEY,
    installation_id       UUID NOT NULL,
    organization_id       VARCHAR(36) NOT NULL,
    canonical_subject_key VARCHAR(128) NOT NULL,
    automatic_arm         VARCHAR(32) NOT NULL,
    manual_override       VARCHAR(32),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT blind_router_experiment_assignments_user_installation_fk
        FOREIGN KEY (router_user_id, installation_id)
        REFERENCES router.model_router_users (id, installation_id) ON DELETE CASCADE,
    CONSTRAINT blind_router_experiment_assignments_automatic_arm
        CHECK (automatic_arm IN ('router_on', 'passthrough')),
    CONSTRAINT blind_router_experiment_assignments_manual_override
        CHECK (manual_override IS NULL OR manual_override IN ('router_on', 'passthrough'))
);

CREATE INDEX blind_router_experiment_assignments_installation_id_idx
    ON router.blind_router_experiment_assignments (installation_id);

CREATE INDEX blind_router_experiment_assignments_organization_id_idx
    ON router.blind_router_experiment_assignments (organization_id);

CREATE INDEX blind_router_experiment_assignments_subject_idx
    ON router.blind_router_experiment_assignments (installation_id, canonical_subject_key);

ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN blind_experiment_arm VARCHAR(32),
    ADD COLUMN blind_experiment_assignment_source VARCHAR(32),
    ADD COLUMN blind_experiment_subject_key VARCHAR(128),
    ADD CONSTRAINT model_router_request_telemetry_blind_experiment_arm
        CHECK (blind_experiment_arm IS NULL OR blind_experiment_arm IN ('router_on', 'passthrough')),
    ADD CONSTRAINT model_router_request_telemetry_blind_experiment_assignment_source
        CHECK (blind_experiment_assignment_source IS NULL OR blind_experiment_assignment_source IN ('automatic', 'manual'));

COMMIT;
