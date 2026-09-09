-- Records one upstream attempt of an inference operation. Attempts are keyed
-- by (installation_id, request_id, operation_id, attempt_index) so they never
-- collide with the request summary's (installation_id, request_id, span_type)
-- uniqueness, and a request with several operations (main turn plus a handover
-- summary) keeps each operation's attempt order intact. Usage columns are NULL
-- when usage_known is false; a zero is never fabricated for unknown usage.
-- Re-delivery of the same attempt is a no-op.
-- name: InsertInferenceAttempt :exec
INSERT INTO router.model_router_inference_attempts (
    installation_id,
    request_id,
    operation_id,
    attempt_index,
    purpose,
    policy_id,
    registry_revision,
    policy_revision,
    model,
    provider,
    binding_index,
    outcome,
    failure_reason,
    upstream_status_code,
    latency_ms,
    usage_known,
    input_tokens,
    output_tokens,
    cache_creation_tokens,
    cache_read_tokens,
    cost_usd_micros
) VALUES (
    @installation_id::uuid,
    @request_id::varchar,
    @operation_id::varchar,
    @attempt_index::int,
    @purpose::varchar,
    @policy_id::varchar,
    @registry_revision::varchar,
    @policy_revision::varchar,
    @model::varchar,
    @provider::varchar,
    @binding_index::int,
    @outcome::varchar,
    sqlc.narg('failure_reason')::varchar,
    sqlc.narg('upstream_status_code')::int,
    sqlc.narg('latency_ms')::bigint,
    @usage_known::boolean,
    sqlc.narg('input_tokens')::int,
    sqlc.narg('output_tokens')::int,
    sqlc.narg('cache_creation_tokens')::int,
    sqlc.narg('cache_read_tokens')::int,
    sqlc.narg('cost_usd_micros')::bigint
)
ON CONFLICT (installation_id, request_id, operation_id, attempt_index) DO NOTHING;

-- Returns every attempt of one request in attempt order, grouped by operation,
-- for diagnostics and the runtime inspection API.
-- name: GetInferenceAttempts :many
SELECT *
FROM router.model_router_inference_attempts
WHERE installation_id = @installation_id::uuid
  AND request_id = @request_id::varchar
ORDER BY operation_id, attempt_index;
