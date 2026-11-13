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
