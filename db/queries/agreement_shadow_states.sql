-- Load the two isolated agreement simulations for a single pin bucket.
-- name: GetAgreementShadowState :one
SELECT revision, state
FROM router.agreement_shadow_states
WHERE installation_id = @installation_id::uuid
  AND session_key = @session_key::bytea
  AND role = @role::varchar;

-- Create the first snapshot only when another worker has not already done so.
-- name: CreateAgreementShadowState :execrows
INSERT INTO router.agreement_shadow_states (installation_id, session_key, role, revision, state)
VALUES (@installation_id::uuid, @session_key::bytea, @role::varchar, 1, @state::jsonb)
ON CONFLICT (installation_id, session_key, role) DO NOTHING;

-- Advance only the snapshot read by this worker; stale writers must re-evaluate.
-- name: UpdateAgreementShadowState :execrows
UPDATE router.agreement_shadow_states
SET revision = revision + 1, state = @state::jsonb, updated_at = now()
WHERE installation_id = @installation_id::uuid
  AND session_key = @session_key::bytea
  AND role = @role::varchar
  AND revision = @expected_revision::bigint;

-- Expire abandoned comparison state independently of the live session pin.
-- name: SweepAgreementShadowStates :exec
DELETE FROM router.agreement_shadow_states WHERE updated_at < now() - interval '7 days';
