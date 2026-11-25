BEGIN;

ALTER TABLE router.blind_router_experiment_configurations
    DROP COLUMN control_model;

COMMIT;
