BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN classifier_artifact_id,
    DROP COLUMN classifier_artifact_sha256,
    DROP COLUMN classifier_predicted_label,
    DROP COLUMN classifier_class_order,
    DROP COLUMN classifier_probabilities,
    DROP COLUMN selection_policy_release_id,
    DROP COLUMN selection_policy_sha256,
    DROP COLUMN selection_head_generation,
    DROP COLUMN selection_trace;

COMMIT;
