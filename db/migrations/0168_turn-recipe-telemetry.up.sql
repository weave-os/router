-- Records the recipe that served each turn, so a beta-versus-stable comparison
-- keys on what actually ran. A retained conversation runs the current worker
-- code with its historical policy and classifier, so the release ID alone does
-- not identify the recipe. Also adds the serving identity that policy shadow
-- rows need for the same comparison, and the installation opt-in for the
-- release and policy response headers. Every column is nullable or defaults to
-- false, so no existing behavior changes.
BEGIN;

ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN worker_revision VARCHAR,
    ADD COLUMN worker_image_digest VARCHAR,
    ADD COLUMN worker_configuration_sha256 VARCHAR,
    ADD COLUMN serving_policy_sha256 VARCHAR,
    ADD COLUMN effective_strategy VARCHAR,
    ADD COLUMN classifier_release_id VARCHAR,
    ADD COLUMN classifier_enabled_variants VARCHAR,
    ADD COLUMN flag_overrides_sha256 VARCHAR;

ALTER TABLE router.policy_shadow_decisions
    ADD COLUMN serving_target VARCHAR,
    ADD COLUMN requested_model VARCHAR,
    ADD COLUMN serving_release_id VARCHAR;

ALTER TABLE router.policy_shadow_decisions
    ADD CONSTRAINT policy_shadow_decisions_serving_target_check
        CHECK (serving_target IS NULL OR serving_target IN ('staging', 'prod/stable', 'prod/beta', 'prod/weave-internal'));

ALTER TABLE router.model_router_installations
    ADD COLUMN serving_release_headers_enabled BOOLEAN NOT NULL DEFAULT false;

COMMIT;
