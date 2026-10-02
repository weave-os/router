BEGIN;

ALTER TABLE router.model_router_request_telemetry DROP COLUMN experiment_snapshot_id;
ALTER TABLE router.installation_routing_policies DROP COLUMN experiment_snapshot_id;
ALTER TABLE router.blind_router_experiment_configurations DROP COLUMN experiment_snapshot_id;
DROP TABLE router.experiment_settings_snapshots;

COMMIT;
