BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN reporting_subject_key,
    DROP COLUMN reporting_bypass_reason,
    DROP COLUMN reporting_treatment_applied,
    DROP COLUMN reporting_assigned_arm,
    DROP COLUMN reporting_revision,
    DROP COLUMN reporting_experiment_id,
    DROP COLUMN reporting_mode,
    DROP COLUMN reporting_schema_version;

ALTER TABLE router.installation_routing_policies
    DROP COLUMN reporting_updated_at,
    DROP COLUMN reporting_experiment_id;

ALTER TABLE router.blind_router_experiment_configurations
    DROP COLUMN reporting_updated_at,
    DROP COLUMN reporting_revision,
    DROP COLUMN reporting_experiment_id;

COMMIT;
