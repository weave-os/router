-- name: DeleteExpiredEscalationSession :exec
-- Expiry starts a new lifetime and cascades its prior boundary records.
DELETE FROM router.escalation_sessions
WHERE scope = @scope::bytea AND expires_at <= clock_timestamp();

-- name: UpsertEscalationSessionClaim :one
-- Only an expired or released lease can be acquired; no lock spans inference.
INSERT INTO router.escalation_sessions (
    scope, installation_id, lease_token, lease_boundary, lease_until, expires_at
) VALUES (
    @scope::bytea, @installation_id::uuid, @lease_token::uuid, @boundary::bytea,
    clock_timestamp() + interval '15 seconds', clock_timestamp() + interval '24 hours'
)
ON CONFLICT (scope) DO UPDATE SET
    lease_token = EXCLUDED.lease_token,
    lease_until = EXCLUDED.lease_until,
    lease_boundary = EXCLUDED.lease_boundary,
    session_state = CASE WHEN router.escalation_sessions.continuity_broken
        THEN router.escalation_sessions.session_state || '{"feature_state":null,"feature_turns":0,"previous_outcome":null}'::jsonb
        ELSE router.escalation_sessions.session_state END,
    continuity_broken = false,
    expires_at = EXCLUDED.expires_at
WHERE router.escalation_sessions.installation_id = EXCLUDED.installation_id
    AND router.escalation_sessions.expires_at > clock_timestamp()
    AND (router.escalation_sessions.lease_until IS NULL
         OR router.escalation_sessions.lease_until <= clock_timestamp())
RETURNING session_state;

-- name: GetEscalationSessionExpired :one
-- A failed claim retries only if expiry crossed the delete/claim boundary.
SELECT expires_at <= clock_timestamp() AS expired
FROM router.escalation_sessions WHERE scope = @scope::bytea;

-- name: GetEscalationCheckpoint :one
-- A checkpoint belongs to the current unexpired session lifetime.
SELECT c.checkpoint FROM router.escalation_checkpoints c
JOIN router.escalation_sessions s ON s.scope = c.scope
WHERE c.scope = @scope::bytea AND c.boundary = @boundary::bytea
    AND s.expires_at > clock_timestamp();

-- name: UpdateEscalationSessionCommit :execrows
-- The nonce and deadline prevent a late inference from overwriting a successor.
UPDATE router.escalation_sessions SET
    ordinal = @ordinal::bigint,
    session_state = CASE WHEN continuity_broken
        THEN @session_state::jsonb || '{"feature_state":null,"feature_turns":0,"previous_outcome":null}'::jsonb
        ELSE @session_state::jsonb END,
    continuity_broken = false,
    lease_token = NULL,
    lease_until = NULL,
    lease_boundary = NULL,
    expires_at = clock_timestamp() + interval '24 hours'
WHERE scope = @scope::bytea AND lease_token = @lease_token::uuid
    AND lease_until > clock_timestamp() AND expires_at > clock_timestamp()
    AND ordinal + 1 = @ordinal::bigint;

-- name: InsertEscalationCheckpoint :exec
-- The boundary uniqueness constraint makes repeated intervention commits atomic failures.
INSERT INTO router.escalation_checkpoints (scope, boundary, checkpoint)
VALUES (@scope::bytea, @boundary::bytea, @checkpoint::jsonb);

-- name: UpdateEscalationSessionRelease :exec
-- Releasing an old nonce cannot cancel a later owner's lease.
UPDATE router.escalation_sessions SET lease_token = NULL, lease_until = NULL, lease_boundary = NULL
WHERE scope = @scope::bytea AND lease_token = @lease_token::uuid;

-- name: UpdateEscalationSessionOutcome :exec
-- A completed response never changes an observation already claimed by a successor.
UPDATE router.escalation_sessions
SET session_state = jsonb_set(session_state, '{previous_outcome}', @previous_outcome::jsonb)
WHERE scope = @scope::bytea AND ordinal = @ordinal::bigint
    AND lease_token IS NULL AND expires_at > clock_timestamp()
    AND session_state->'feature_state' IS NOT NULL
    AND session_state->'feature_state' <> 'null'::jsonb;

-- name: GetEscalationSessionForInvalidation :one
-- Lock before checking checkpoints in a fresh READ COMMITTED statement snapshot.
SELECT scope FROM router.escalation_sessions
WHERE scope = @scope::bytea AND expires_at > clock_timestamp()
FOR UPDATE;

-- name: UpdateEscalationSessionInvalidated :exec
-- A failed owner resets and releases atomically; other live observers retain their lease.
UPDATE router.escalation_sessions SET
    session_state = CASE WHEN lease_until > statement_timestamp() AND lease_token <> @failed_lease_token::uuid THEN session_state
        ELSE session_state || '{"feature_state":null,"feature_turns":0,"previous_outcome":null}'::jsonb END,
    continuity_broken = COALESCE(lease_until > statement_timestamp() AND lease_token <> @failed_lease_token::uuid, false),
    lease_token = CASE WHEN lease_until > statement_timestamp() AND lease_token <> @failed_lease_token::uuid THEN lease_token END,
    lease_boundary = CASE WHEN lease_until > statement_timestamp() AND lease_token <> @failed_lease_token::uuid THEN lease_boundary END,
    lease_until = CASE WHEN lease_until > statement_timestamp() AND lease_token <> @failed_lease_token::uuid THEN lease_until END
WHERE scope = @scope::bytea AND expires_at > statement_timestamp()
    AND (lease_until IS NULL OR lease_until <= statement_timestamp()
         OR lease_boundary IS DISTINCT FROM @boundary::bytea
         OR lease_token = @failed_lease_token::uuid)
    AND NOT EXISTS (
        SELECT 1 FROM router.escalation_checkpoints c
        WHERE c.scope = @scope::bytea AND c.boundary = @boundary::bytea
    );

-- name: DeleteExpiredEscalationSessions :exec
-- Expired feature state and checkpoint identities share one retention boundary.
DELETE FROM router.escalation_sessions WHERE expires_at <= clock_timestamp();

-- name: InsertEscalationContinuation :exec
-- Lock the session while checking ordinal so a successor cannot race the response write.
WITH completed_session AS (
    SELECT scope FROM router.escalation_sessions
    WHERE scope = @scope::bytea AND ordinal = @ordinal::bigint
        AND lease_token IS NULL AND expires_at > clock_timestamp()
    FOR UPDATE
)
INSERT INTO router.escalation_continuations (activation, response_digest, scope, history)
SELECT @activation::bytea, @response_digest::bytea, scope, @history::jsonb
FROM completed_session
ON CONFLICT (activation, response_digest) DO NOTHING;

-- name: GetEscalationContinuation :one
-- Response references are isolated by activation and expire with their owning session.
SELECT c.scope, c.history FROM router.escalation_continuations c
JOIN router.escalation_sessions s ON s.scope = c.scope
WHERE c.activation = @activation::bytea AND c.response_digest = @response_digest::bytea
    AND s.expires_at > clock_timestamp();
