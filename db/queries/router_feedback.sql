-- name: InsertRouterFeedback :one
-- Persist the resolved command before applying its local thumb in the same transaction.
INSERT INTO router.router_feedback (
    id, installation_id, session_key, role, router_user_id, client_app, session_id,
    requested_model, served_model, feedback, rating, suggested_label, source, request_id, route_id,
    external_id, requested_sequence, target_sequence, strategy, served_provider, rollout_id,
    training_allowed, delivery_status
) VALUES (
    @id::uuid,
    @installation_id::uuid,
    @session_key::bytea,
    @role::varchar,
    sqlc.narg('router_user_id')::uuid,
    sqlc.narg('client_app')::text,
    sqlc.narg('session_id')::varchar,
    @requested_model::varchar,
    @served_model::varchar,
    @feedback::text,
    sqlc.narg('rating')::varchar,
    sqlc.narg('suggested_label')::varchar,
    @source::varchar,
    sqlc.narg('request_id')::varchar,
    sqlc.narg('route_id')::varchar,
    @external_id::varchar,
    @requested_sequence::integer,
    @target_sequence::bigint,
    @strategy::varchar,
    @served_provider::varchar,
    @rollout_id::varchar,
    @training_allowed::boolean,
    'pending'
)
ON CONFLICT (id) DO NOTHING
RETURNING *;

-- name: GetRouterFeedback :one
-- Recover immutable acceptance after a lost acknowledgment without reapplying its rating.
SELECT * FROM router.router_feedback WHERE id = @id::uuid;

-- name: UpdateRouterFeedbackClaim :one
-- Expired claims can be recovered by any replica; no lock spans the remote call.
WITH due AS (
    SELECT id FROM router.router_feedback
    WHERE delivery_status = 'pending' AND next_attempt_at <= clock_timestamp()
        AND (lease_until IS NULL OR lease_until <= clock_timestamp())
    ORDER BY next_attempt_at, created_at, id
    LIMIT 1 FOR UPDATE SKIP LOCKED
)
UPDATE router.router_feedback f
SET lease_token = @lease_token::uuid,
    lease_until = clock_timestamp() + sqlc.arg('lease_milliseconds')::bigint * interval '1 millisecond',
    attempts = attempts + 1
FROM due WHERE f.id = due.id
RETURNING f.*;

-- name: UpdateRouterFeedbackFinished :execrows
-- A stale worker cannot settle or reschedule a successor's claim.
UPDATE router.router_feedback
SET delivery_status = @delivery_status::varchar, last_error = @last_error::text,
    next_attempt_at = @next_attempt_at::timestamptz, lease_token = NULL, lease_until = NULL
WHERE id = @id::uuid AND lease_token = @lease_token::uuid AND delivery_status = 'pending';

-- name: GetRouterFeedbackTrainingAllowed :one
-- Local explicit feedback remains saved when the installation opts out or is deleted.
SELECT EXISTS (
    SELECT 1 FROM router.model_router_installations
    WHERE id = @installation_id::uuid AND external_id = @external_id::varchar
        AND deleted_at IS NULL AND ai_training_allowed
)::boolean AS allowed;
