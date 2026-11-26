BEGIN;

ALTER TABLE router.blind_router_experiment_configurations
    ADD COLUMN control_model VARCHAR(128)
        CHECK (control_model IS NULL OR btrim(control_model) <> '');

COMMENT ON COLUMN router.blind_router_experiment_configurations.control_model IS
    'Model every experiment-passthrough turn serves. NULL keeps the client-detected baseline, which exists only for Claude Code and Codex.';

COMMIT;
