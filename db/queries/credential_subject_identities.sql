-- Resolves the person behind a request's X-Weave-User-Email within the
-- authenticated installation. A subject whose installation access was
-- withdrawn or whose projection was revoked resolves to no rows, so the
-- caller falls back to the key's own identity instead of the stale person.
-- name: GetCredentialSubjectForRequestEmail :one
SELECT identity.subject_id
FROM router.credential_subject_identities identity
JOIN router.credential_subject_installations access
    ON access.subject_id = identity.subject_id
    AND access.installation_id = identity.installation_id
    AND access.access_enabled
JOIN router.credential_subjects subject
    ON subject.id = identity.subject_id
    AND subject.revoked_at IS NULL
WHERE identity.installation_id = @installation_id::uuid
  AND identity.email = @email::varchar
  AND identity.revoked_at IS NULL;
