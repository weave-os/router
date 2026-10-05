BEGIN;

ALTER TABLE router.blind_router_experiment_configurations
    ADD COLUMN reporting_experiment_id UUID,
    ADD COLUMN reporting_revision BIGINT CHECK (reporting_revision > 0),
    ADD COLUMN reporting_updated_at TIMESTAMPTZ;

ALTER TABLE router.installation_routing_policies
    ADD COLUMN reporting_experiment_id UUID,
    ADD COLUMN reporting_updated_at TIMESTAMPTZ;

ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN reporting_schema_version SMALLINT,
    ADD COLUMN reporting_mode TEXT,
    ADD COLUMN reporting_experiment_id UUID,
    ADD COLUMN reporting_revision BIGINT,
    ADD COLUMN reporting_assigned_arm TEXT,
    ADD COLUMN reporting_treatment_applied BOOLEAN,
    ADD COLUMN reporting_bypass_reason TEXT,
    ADD COLUMN reporting_subject_key TEXT;

COMMIT;
