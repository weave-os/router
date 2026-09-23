-- Handshake retries return the original thread, including its original release
-- and expiry. A release change cannot rebind an existing idempotency key.
-- name: InsertClassifierThread :one
INSERT INTO router.classifier_threads (
    thread_id, installation_id, credential_sha256, request_id, release, release_sha256, selection_policy_sha256, expires_at
) VALUES (
    @thread_id::uuid, @installation_id::uuid, @credential_sha256::bytea,
    @request_id::uuid, @release::text, @release_sha256::text, @selection_policy_sha256::text, @expires_at::timestamptz
)
ON CONFLICT (installation_id, credential_sha256, request_id)
DO UPDATE SET request_id = EXCLUDED.request_id
RETURNING *;

-- The lock covers inference and prediction commit, preventing competing replicas
-- from producing different historical predictions for one thread.
-- name: GetClassifierThreadForUpdate :one
SELECT * FROM router.classifier_threads
WHERE thread_id = @thread_id::uuid
  AND installation_id = @installation_id::uuid
  AND credential_sha256 = @credential_sha256::bytea
  AND release = @release::text
  AND release_sha256 = @release_sha256::text
  AND selection_policy_sha256 = @selection_policy_sha256::text
  AND expires_at > CURRENT_TIMESTAMP
FOR UPDATE;

-- A prediction lookup is always under an authenticated thread lock.
-- name: GetClassifierPrediction :one
SELECT * FROM router.classifier_predictions
WHERE thread_id = @thread_id::uuid AND turn_digest = @turn_digest::text;

-- Output items belong to the latest admitted call before their input position,
-- not necessarily to a separate invocation per assistant message/text block.
-- name: GetClassifierPredictionBeforeMessage :one
SELECT * FROM router.classifier_predictions
WHERE thread_id = @thread_id::uuid
  AND input_message_count > 0 AND input_message_count <= @message_index::integer
ORDER BY input_message_count DESC LIMIT 1;

-- Root identity survives compaction and rules out re-enrollment of an old thread.
-- name: GetClassifierThreadRoot :one
SELECT turn_digest FROM router.classifier_predictions
WHERE thread_id = @thread_id::uuid AND turn_digest = root_turn_digest;

-- The authenticated thread lock serializes append-only request checkpoints.
-- name: UpdateClassifierThreadPrefix :exec
UPDATE router.classifier_threads
SET prefix_message_count = @prefix_message_count::integer, prefix_digest = @prefix_digest::text
WHERE thread_id = @thread_id::uuid;

-- Unique ordinal and digest constraints reject divergent histories; no overwrite.
-- name: InsertClassifierPrediction :exec
INSERT INTO router.classifier_predictions (
    thread_id, turn_digest, root_turn_digest, user_message_count, tool_call_count,
    tool_error_count, completed_response_count, complexity, probabilities, input_message_count
) VALUES (
    @thread_id::uuid, @turn_digest::text, @root_turn_digest::text,
    @user_message_count::integer, @tool_call_count::integer, @tool_error_count::integer,
    @completed_response_count::integer, @complexity::smallint, @probabilities::double precision[], @input_message_count::integer
);
