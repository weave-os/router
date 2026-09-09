BEGIN;

DROP TABLE router.model_router_inference_attempts;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN inference_purpose,
    DROP COLUMN inference_policy_id,
    DROP COLUMN inference_registry_revision,
    DROP COLUMN inference_policy_revision,
    DROP COLUMN plan_model,
    DROP COLUMN plan_provider,
    DROP COLUMN fallback_reason,
    DROP COLUMN accounting_outcome,
    DROP COLUMN usage_known;

COMMIT;
