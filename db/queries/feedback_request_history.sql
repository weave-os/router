-- name: InsertFeedbackHistoryScope :exec
-- Create the scope lock without resetting existing numbering.
INSERT INTO router.feedback_history_scopes (installation_id, session_key, role)
VALUES (@installation_id::uuid, @session_key::bytea, @role::varchar)
ON CONFLICT DO NOTHING;

-- name: GetFeedbackHistoryScopeForUpdate :one
-- Serialize completions and feedback acceptance only within one logical scope.
SELECT last_sequence FROM router.feedback_history_scopes
WHERE installation_id = @installation_id::uuid AND session_key = @session_key::bytea AND role = @role::varchar
FOR UPDATE;

-- name: GetFeedbackHistoryByRequest :one
-- A repeated completion must not allocate another history position.
SELECT * FROM router.feedback_request_history
WHERE installation_id = @installation_id::uuid AND request_id = @request_id::varchar;

-- name: GetFeedbackHistoryBySequence :one
-- Read the exact selected position after acquiring the scope lock.
SELECT * FROM router.feedback_request_history
WHERE installation_id = @installation_id::uuid AND session_key = @session_key::bytea
    AND role = @role::varchar AND sequence = @sequence::bigint;

-- name: InsertFeedbackRequestHistory :one
-- Request uniqueness also fences duplicate completions arriving under another scope.
INSERT INTO router.feedback_request_history (
    installation_id, session_key, role, sequence, request_id, served_model, served_provider, strategy, route_id, training_allowed
) VALUES (
    @installation_id::uuid, @session_key::bytea, @role::varchar, @sequence::bigint,
    @request_id::varchar, @served_model::varchar, @served_provider::varchar,
    @strategy::varchar, @route_id::varchar, @training_allowed::boolean
)
ON CONFLICT (installation_id, request_id) DO NOTHING
RETURNING sequence;

-- name: UpdateFeedbackHistorySequence :exec
-- Advance only after inserting history in the same scope-locked transaction.
UPDATE router.feedback_history_scopes SET last_sequence = @sequence::bigint
WHERE installation_id = @installation_id::uuid AND session_key = @session_key::bytea AND role = @role::varchar;
