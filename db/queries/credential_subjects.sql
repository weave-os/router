-- Setup creates a pending subject and installation access in the key-issuance transaction.
-- name: InsertCredentialSubject :one
INSERT INTO router.credential_subjects DEFAULT VALUES RETURNING *;

-- Access remains disabled until the authenticated control-plane mapping has succeeded.
-- name: InsertCredentialSubjectInstallation :exec
INSERT INTO router.credential_subject_installations(subject_id, installation_id)
VALUES (@subject_id::uuid, @installation_id::uuid);

-- Personal issuance never rebinds or rotates an installation-shared key.
-- name: InsertPersonalRoutingKey :one
INSERT INTO router.model_router_api_keys (
    installation_id, external_id, name, key_prefix, key_hash, key_suffix, scope, credential_subject_id
) VALUES (
    @installation_id::uuid, @external_id::varchar, sqlc.narg(name)::varchar,
    @key_prefix::varchar, @key_hash::varchar, @key_suffix::varchar, 'routing', @subject_id::uuid
) RETURNING *;

-- The backend calls completion only after persisting its authenticated account mapping.
-- name: UpdateCredentialSubjectProjection :execrows
UPDATE router.credential_subjects
SET projection_complete = true, enrollment_generation = enrollment_generation + 1
WHERE id = @subject_id::uuid AND revoked_at IS NULL AND NOT projection_complete;

-- Installation access changes take the installation lock before updating this row.
-- name: UpdateCredentialSubjectInstallationAccess :execrows
UPDATE router.credential_subject_installations SET access_enabled = @access_enabled::boolean
WHERE subject_id = @subject_id::uuid AND installation_id = @installation_id::uuid;

-- Enrollment is effective only for fully projected, non-revoked personal subjects.
-- name: UpdateCredentialSubjectEnrollment :execrows
UPDATE router.credential_subjects
SET internal_enrolled = @enrolled::boolean, enrollment_generation = enrollment_generation + 1
WHERE id = @subject_id::uuid AND projection_complete AND revoked_at IS NULL
  AND internal_enrolled IS DISTINCT FROM @enrolled::boolean;

-- Revocation withdraws internal eligibility and changes generation without rotating shared keys.
-- name: UpdateCredentialSubjectRevoked :execrows
UPDATE router.credential_subjects
SET revoked_at = clock_timestamp(), internal_enrolled = false, enrollment_generation = enrollment_generation + 1
WHERE id = @subject_id::uuid AND revoked_at IS NULL;

-- Lock all affected installations in deterministic order before profile/access projection.
-- name: GetServingInstallationsForProjection :many
SELECT id FROM router.model_router_installations
WHERE id = ANY(@installation_ids::uuid[]) AND external_id = @external_id::varchar AND deleted_at IS NULL
ORDER BY id FOR UPDATE;

-- Projection is transaction-wide for the organization's installation set; no mutable release pointer here.
-- name: UpsertServingProfileAssignment :execrows
INSERT INTO router.installation_profile_assignments(installation_id, profile_key, assignment_generation)
VALUES (@installation_id::uuid, sqlc.narg(profile_key)::uuid, 1)
ON CONFLICT (installation_id) DO UPDATE SET
    profile_key = EXCLUDED.profile_key,
    assignment_generation = router.installation_profile_assignments.assignment_generation + 1,
    updated_at = clock_timestamp()
WHERE router.installation_profile_assignments.profile_key IS DISTINCT FROM EXCLUDED.profile_key;

-- Subject profile writers lock subject access before making a precedence source visible to admission.
-- name: GetCredentialSubjectInstallationForProfileProjection :one
SELECT subject_id
FROM router.credential_subject_installations
WHERE subject_id = @subject_id::uuid AND installation_id = @installation_id::uuid
FOR UPDATE;

-- Projection retries preserve the previous effective profile until a desired generation becomes effective.
-- name: UpsertCredentialSubjectProfileAssignment :one
INSERT INTO router.credential_subject_profile_assignments (
    subject_id,
    installation_id,
    assignment_source,
    assignment_state,
    desired_generation,
    effective_generation,
    desired_profile_key,
    effective_profile_key,
    evidence_id,
    projected_at,
    effective_at,
    last_failure_detail
) VALUES (
    @subject_id::uuid,
    @installation_id::uuid,
    @assignment_source::varchar,
    @assignment_state::varchar,
    @desired_generation::bigint,
    CASE
        WHEN @assignment_state::varchar IN ('effective', 'default_following', 'deliberately_unassigned')
            THEN @desired_generation::bigint
        ELSE 0
    END,
    sqlc.narg(desired_profile_key)::uuid,
    CASE
        WHEN @assignment_state::varchar IN ('effective', 'default_following', 'deliberately_unassigned')
            THEN sqlc.narg(desired_profile_key)::uuid
        ELSE NULL::uuid
    END,
    @evidence_id::varchar,
    clock_timestamp(),
    CASE
        WHEN @assignment_state::varchar IN ('effective', 'default_following', 'deliberately_unassigned')
            THEN clock_timestamp()
        ELSE NULL::timestamptz
    END,
    sqlc.narg(last_failure_detail)::varchar
)
ON CONFLICT (subject_id, installation_id, assignment_source) DO UPDATE SET
    assignment_state = EXCLUDED.assignment_state,
    desired_generation = EXCLUDED.desired_generation,
    desired_profile_key = EXCLUDED.desired_profile_key,
    effective_generation = CASE
        WHEN EXCLUDED.assignment_state IN ('effective', 'default_following', 'deliberately_unassigned')
            THEN EXCLUDED.desired_generation
        ELSE router.credential_subject_profile_assignments.effective_generation
    END,
    effective_profile_key = CASE
        WHEN EXCLUDED.assignment_state IN ('effective', 'default_following', 'deliberately_unassigned')
            THEN EXCLUDED.desired_profile_key
        ELSE router.credential_subject_profile_assignments.effective_profile_key
    END,
    evidence_id = EXCLUDED.evidence_id,
    projection_attempts = router.credential_subject_profile_assignments.projection_attempts + 1,
    projected_at = EXCLUDED.projected_at,
    effective_at = CASE
        WHEN EXCLUDED.assignment_state IN ('effective', 'default_following', 'deliberately_unassigned')
            THEN EXCLUDED.effective_at
        ELSE router.credential_subject_profile_assignments.effective_at
    END,
    last_failure_detail = EXCLUDED.last_failure_detail,
    updated_at = clock_timestamp()
WHERE router.credential_subject_profile_assignments.desired_generation < EXCLUDED.desired_generation
   OR (
       router.credential_subject_profile_assignments.desired_generation = EXCLUDED.desired_generation
       AND router.credential_subject_profile_assignments.desired_profile_key IS NOT DISTINCT FROM EXCLUDED.desired_profile_key
   )
RETURNING *;
