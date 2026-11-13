BEGIN;

CREATE EXTENSION IF NOT EXISTS btree_gist WITH SCHEMA router;

ALTER TABLE router.blind_router_experiment_configurations
    ADD COLUMN cohort_experiment_id UUID,
    ADD COLUMN cohort_starts_at TIMESTAMPTZ,
    ADD COLUMN cohort_ends_at TIMESTAMPTZ,
    ADD COLUMN cohort_timezone TEXT,
    ADD COLUMN cohort_revision INTEGER,
    ADD CONSTRAINT blind_router_experiment_cohort_complete CHECK (
        (cohort_experiment_id IS NULL AND cohort_starts_at IS NULL AND cohort_ends_at IS NULL
            AND cohort_timezone IS NULL AND cohort_revision IS NULL)
        OR (cohort_experiment_id IS NOT NULL AND cohort_starts_at IS NOT NULL
            AND cohort_ends_at IS NOT NULL AND cohort_timezone IS NOT NULL AND cohort_revision > 0
            AND cohort_starts_at < cohort_ends_at)
    );

ALTER TABLE router.blind_router_experiment_configurations
    ADD CONSTRAINT blind_router_experiment_cohort_identity UNIQUE (installation_id, cohort_experiment_id);

CREATE TABLE router.blind_router_experiment_groups (
    installation_id UUID NOT NULL,
    experiment_id UUID NOT NULL,
    group_id SMALLINT NOT NULL CHECK (group_id > 0),
    label TEXT NOT NULL CHECK (length(btrim(label)) > 0),
    PRIMARY KEY (installation_id, experiment_id, group_id),
    UNIQUE (installation_id, experiment_id, label),
    FOREIGN KEY (installation_id, experiment_id)
        REFERENCES router.blind_router_experiment_configurations (installation_id, cohort_experiment_id)
        ON DELETE CASCADE
);

CREATE TABLE router.blind_router_experiment_group_memberships (
    installation_id UUID NOT NULL,
    experiment_id UUID NOT NULL,
    canonical_subject_key VARCHAR(128) NOT NULL,
    group_id SMALLINT NOT NULL,
    PRIMARY KEY (installation_id, experiment_id, canonical_subject_key),
    FOREIGN KEY (installation_id, experiment_id, group_id)
        REFERENCES router.blind_router_experiment_groups (installation_id, experiment_id, group_id)
        ON DELETE CASCADE
);

CREATE INDEX blind_router_experiment_group_memberships_group_idx
    ON router.blind_router_experiment_group_memberships (installation_id, experiment_id, group_id);

CREATE TABLE router.blind_router_experiment_schedule (
    installation_id UUID NOT NULL,
    experiment_id UUID NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    group_id SMALLINT NOT NULL,
    phase_index SMALLINT NOT NULL CHECK (phase_index > 0),
    starts_at TIMESTAMPTZ NOT NULL,
    ends_at TIMESTAMPTZ NOT NULL,
    arm VARCHAR(32) NOT NULL CHECK (arm IN ('router_on', 'passthrough')),
    PRIMARY KEY (installation_id, experiment_id, revision, group_id, phase_index),
    FOREIGN KEY (installation_id, experiment_id, group_id)
        REFERENCES router.blind_router_experiment_groups (installation_id, experiment_id, group_id)
        ON DELETE CASCADE,
    CHECK (starts_at < ends_at),
    CONSTRAINT blind_router_experiment_schedule_no_overlap EXCLUDE USING gist (
        installation_id WITH =,
        experiment_id WITH =,
        revision WITH =,
        group_id WITH =,
        tstzrange(starts_at, ends_at, '[)') WITH &&
    )
);

CREATE INDEX blind_router_experiment_schedule_window_idx
    ON router.blind_router_experiment_schedule (installation_id, experiment_id, revision, starts_at);

-- Emergency changes never mutate group membership or the weekly schedule.
-- Revocation preserves the original interval and records who ended it early.
CREATE TABLE router.blind_router_experiment_emergency_overrides (
    id UUID PRIMARY KEY,
    installation_id UUID NOT NULL,
    experiment_id UUID NOT NULL,
    canonical_subject_key VARCHAR(128) NOT NULL,
    arm VARCHAR(32) NOT NULL CHECK (arm IN ('router_on', 'passthrough')),
    starts_at TIMESTAMPTZ NOT NULL,
    ends_at TIMESTAMPTZ NOT NULL,
    reason TEXT NOT NULL CHECK (length(btrim(reason)) > 0),
    created_by VARCHAR(128) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ,
    revoked_by VARCHAR(128),
    revocation_reason TEXT,
    FOREIGN KEY (installation_id, experiment_id, canonical_subject_key)
        REFERENCES router.blind_router_experiment_group_memberships
            (installation_id, experiment_id, canonical_subject_key),
    CHECK (starts_at < ends_at),
    CHECK ((revoked_at IS NULL AND revoked_by IS NULL AND revocation_reason IS NULL)
        OR (revoked_at IS NOT NULL AND revoked_by IS NOT NULL
            AND length(btrim(revocation_reason)) > 0))
);

CREATE INDEX blind_router_experiment_emergency_overrides_subject_idx
    ON router.blind_router_experiment_emergency_overrides
    (installation_id, experiment_id, canonical_subject_key, starts_at);

ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN cohort_experiment_id UUID,
    ADD COLUMN cohort_group_id SMALLINT,
    ADD COLUMN cohort_phase_index SMALLINT,
    ADD COLUMN cohort_revision INTEGER,
    ADD COLUMN cohort_scheduled_arm VARCHAR(32),
    ADD COLUMN cohort_treatment_applied BOOLEAN,
    ADD COLUMN cohort_bypass_reason VARCHAR(32),
    ADD CONSTRAINT model_router_request_cohort_scheduled_arm
        CHECK (cohort_scheduled_arm IS NULL OR cohort_scheduled_arm IN ('router_on', 'passthrough'));

COMMIT;
