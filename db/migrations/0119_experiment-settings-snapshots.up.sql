BEGIN;

CREATE TABLE router.experiment_settings_snapshots (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    installation_id UUID NOT NULL REFERENCES router.model_router_installations(id),
    experiment_id UUID NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    mode TEXT NOT NULL CHECK (mode IN ('percentage', 'teams')),
    settings JSONB NOT NULL CHECK (jsonb_typeof(settings) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (installation_id, experiment_id, revision)
);

ALTER TABLE router.blind_router_experiment_configurations
    ADD COLUMN experiment_snapshot_id BIGINT REFERENCES router.experiment_settings_snapshots(id);
ALTER TABLE router.installation_routing_policies
    ADD COLUMN experiment_snapshot_id BIGINT REFERENCES router.experiment_settings_snapshots(id);
ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN experiment_snapshot_id BIGINT REFERENCES router.experiment_settings_snapshots(id);

COMMIT;
