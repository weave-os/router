BEGIN;

-- Inference-policy provenance on the request summary. The purpose names the
-- typed operation (anthropic_messages, handover_summary, ...); policy id and
-- the registry/policy revisions identify the reviewed policy entry that
-- produced the plan, so telemetry, generated docs, runtime inspection, and the
-- WorkWeave UI can be compared on one revision. plan_model/plan_provider are
-- the policy-selected target; decision_model/decision_provider remain the
-- served target. fallback_reason is set only when the served target differs
-- from the plan. accounting_outcome states how usage was accounted:
-- 'billed', 'unbilled', 'usage_unknown', or 'failed'. usage_known is FALSE when
-- the upstream reported no usage, in which case the token/cost columns are
-- unmeasured, not zero. All NULL on rows written before the columns existed
-- and on paths not yet migrated to the executor.
ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN inference_purpose VARCHAR,
    ADD COLUMN inference_policy_id VARCHAR,
    ADD COLUMN inference_registry_revision VARCHAR,
    ADD COLUMN inference_policy_revision VARCHAR,
    ADD COLUMN plan_model VARCHAR,
    ADD COLUMN plan_provider VARCHAR,
    ADD COLUMN fallback_reason VARCHAR,
    ADD COLUMN accounting_outcome VARCHAR,
    ADD COLUMN usage_known BOOLEAN;

-- One row per upstream attempt an executor made for an operation, ordered by
-- attempt_index. The summary row stays unique on (installation_id,
-- request_id, span_type); attempts are keyed by (installation_id, request_id,
-- operation_id, attempt_index) so a request that performs several operations
-- (the main turn plus a handover summary) and retries within each never
-- collide with the summary or with each other. outcome is 'served',
-- 'failed', 'skipped', or 'aborted'; failure_reason is a bounded machine
-- reason (upstream status class, timeout, capability rejection), never an
-- upstream body. usage_known FALSE means the usage columns are unmeasured.
CREATE TABLE router.model_router_inference_attempts (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    installation_id      UUID NOT NULL REFERENCES router.model_router_installations(id) ON DELETE CASCADE,
    request_id           VARCHAR NOT NULL,
    operation_id         VARCHAR NOT NULL,
    attempt_index        INT NOT NULL,
    purpose              VARCHAR NOT NULL,
    policy_id            VARCHAR NOT NULL,
    registry_revision    VARCHAR NOT NULL,
    policy_revision      VARCHAR NOT NULL,
    model                VARCHAR NOT NULL,
    provider             VARCHAR NOT NULL,
    binding_index        INT NOT NULL,
    outcome              VARCHAR NOT NULL,
    failure_reason       VARCHAR,
    upstream_status_code INT,
    latency_ms           BIGINT,
    usage_known          BOOLEAN NOT NULL,
    input_tokens         INT,
    output_tokens        INT,
    cache_creation_tokens INT,
    cache_read_tokens    INT,
    cost_usd_micros      BIGINT,
    UNIQUE (installation_id, request_id, operation_id, attempt_index)
);

CREATE INDEX model_router_inference_attempts_installation_id_created_at_idx
    ON router.model_router_inference_attempts (installation_id, created_at DESC);

COMMENT ON TABLE router.model_router_inference_attempts IS 'Ordered upstream attempts per inference operation; joins to model_router_request_telemetry on (installation_id, request_id)';
COMMENT ON COLUMN router.model_router_inference_attempts.usage_known IS 'FALSE when the upstream reported no usage; token and cost columns are then unmeasured rather than zero';

COMMIT;
