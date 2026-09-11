BEGIN;

-- Classifier and Go selection-policy identities are independent artifacts in
-- one atomic release. Persist both, plus the content-free classifier facts and
-- Go selection trace, so a request row can replay the exact decision without
-- consulting sidecar logs or inferring policy identity from roster_version.
ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN classifier_artifact_id VARCHAR,
    ADD COLUMN classifier_artifact_sha256 VARCHAR,
    ADD COLUMN classifier_predicted_label VARCHAR,
    ADD COLUMN classifier_class_order VARCHAR[],
    ADD COLUMN classifier_probabilities JSONB,
    ADD COLUMN selection_policy_release_id VARCHAR,
    ADD COLUMN selection_policy_sha256 VARCHAR,
    ADD COLUMN selection_head_generation BIGINT,
    ADD COLUMN selection_trace JSONB;

COMMIT;
