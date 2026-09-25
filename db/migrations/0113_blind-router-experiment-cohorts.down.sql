BEGIN;

DROP TABLE router.blind_router_experiment_emergency_overrides;

ALTER TABLE router.model_router_request_telemetry
    DROP CONSTRAINT model_router_request_cohort_scheduled_arm,
    DROP COLUMN cohort_bypass_reason,
    DROP COLUMN cohort_treatment_applied,
    DROP COLUMN cohort_scheduled_arm,
    DROP COLUMN cohort_revision,
    DROP COLUMN cohort_phase_index,
    DROP COLUMN cohort_group_id,
    DROP COLUMN cohort_experiment_id;

DROP TABLE router.blind_router_experiment_schedule;
DROP TABLE router.blind_router_experiment_group_memberships;
DROP TABLE router.blind_router_experiment_groups;

ALTER TABLE router.blind_router_experiment_configurations
    DROP CONSTRAINT blind_router_experiment_cohort_complete,
    DROP CONSTRAINT blind_router_experiment_cohort_identity,
    DROP COLUMN cohort_revision,
    DROP COLUMN cohort_timezone,
    DROP COLUMN cohort_ends_at,
    DROP COLUMN cohort_starts_at,
    DROP COLUMN cohort_experiment_id;

COMMIT;
