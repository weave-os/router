-- name: DeleteFeedbackCheckFixture :execrows
-- Remove only the disposable installation created by the database check.
DELETE FROM router.model_router_installations
WHERE id = @installation_id::uuid AND external_id = @external_id::varchar;

-- name: GetFeedbackCheckRemaining :one
-- Verify physical cascade cleanup of every fixture-owned feedback row.
SELECT (
    (SELECT count(*) FROM router.model_router_installations WHERE id = @installation_id::uuid)
    + (SELECT count(*) FROM router.feedback_history_scopes WHERE installation_id = @installation_id::uuid)
    + (SELECT count(*) FROM router.feedback_request_history WHERE installation_id = @installation_id::uuid)
    + (SELECT count(*) FROM router.router_feedback WHERE installation_id = @installation_id::uuid)
    + (SELECT count(*) FROM router.request_feedback WHERE installation_id = @installation_id::uuid)
)::bigint AS remaining;

-- name: GetFeedbackCheckPID :one
-- Capture the dedicated reader connection before starting the blocked acceptance.
SELECT pg_backend_pid()::integer AS pid;

-- name: GetFeedbackCheckBlocked :one
-- Count blockers of exactly the reader connection used by this check.
SELECT cardinality(pg_blocking_pids(@pid::integer))::bigint AS blockers;

-- name: GetFeedbackCheckRatingForUpdate :one
-- Hold this fixture's existing rating to force acceptance to fail after command insertion.
SELECT f.request_id FROM router.request_feedback f
JOIN router.model_router_installations i ON i.id = f.installation_id
WHERE i.id = @installation_id::uuid AND i.external_id = @external_id::varchar AND f.request_id = @request_id::varchar
FOR UPDATE OF f;

-- name: UpdateFeedbackCheckDue :exec
-- Expire only the fixture's pending work, including abandoned leases.
UPDATE router.router_feedback f
SET next_attempt_at = clock_timestamp() - interval '1 second',
    lease_until = CASE WHEN lease_token IS NULL THEN NULL ELSE clock_timestamp() - interval '1 second' END
FROM router.model_router_installations i
WHERE f.installation_id = i.id AND i.id = @installation_id::uuid AND i.external_id = @external_id::varchar
    AND f.delivery_status = 'pending';

-- name: UpdateFeedbackCheckPermission :exec
-- Change training consent only for the disposable installation.
UPDATE router.model_router_installations SET ai_training_allowed = @allowed::boolean
WHERE id = @installation_id::uuid AND external_id = @external_id::varchar;

-- name: GetFeedbackCheckHistory :many
-- Inspect numbering for one fixture-owned logical scope.
SELECT h.* FROM router.feedback_request_history h
JOIN router.model_router_installations i ON i.id = h.installation_id
WHERE i.id = @installation_id::uuid AND i.external_id = @external_id::varchar
    AND h.session_key = @session_key::bytea AND h.role = @role::varchar
ORDER BY h.sequence;
