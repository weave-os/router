BEGIN;

ALTER TABLE router.model_router_installations
    DROP COLUMN serving_release_headers_enabled;

ALTER TABLE router.policy_shadow_decisions
    DROP CONSTRAINT policy_shadow_decisions_serving_target_check;

ALTER TABLE router.policy_shadow_decisions
    DROP COLUMN serving_release_id,
    DROP COLUMN requested_model,
    DROP COLUMN serving_target;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN flag_overrides_sha256,
    DROP COLUMN classifier_enabled_variants,
    DROP COLUMN classifier_release_id,
    DROP COLUMN effective_strategy,
    DROP COLUMN serving_policy_sha256,
    DROP COLUMN worker_configuration_sha256,
    DROP COLUMN worker_image_digest,
    DROP COLUMN worker_revision;

COMMIT;
