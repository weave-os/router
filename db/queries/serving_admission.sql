-- Admission shares the installation lock; assignment changes take its exclusive lock first.
-- name: GetServingInstallationForAdmission :one
SELECT id FROM router.model_router_installations
WHERE id = @installation_id::uuid AND deleted_at IS NULL
FOR SHARE;

-- Read the credential again after authentication so cache staleness cannot bypass revocation.
-- name: GetServingCredentialForAdmission :one
SELECT id, credential_subject_id, scope
FROM router.model_router_api_keys
WHERE id = @api_key_id::uuid AND installation_id = @installation_id::uuid AND deleted_at IS NULL
FOR SHARE;

-- Subject state is authoritative on each admission, independent of client metadata.
-- name: GetServingSubjectForAdmission :one
SELECT sqlc.embed(subject), access.access_enabled
FROM router.credential_subjects subject
JOIN router.credential_subject_installations access ON access.subject_id = subject.id
WHERE subject.id = @subject_id::uuid AND access.installation_id = @installation_id::uuid
FOR SHARE OF subject, access;

-- A missing assignment means follow the lane default; the installation lock protects absence too.
-- name: GetServingProfileAssignment :one
SELECT profile_key, assignment_generation
FROM router.installation_profile_assignments
WHERE installation_id = @installation_id::uuid;

-- name: GetActiveServingSubscriberPlan :one
-- Active individual plans override installation assignments before serving selection.
SELECT version, plan
FROM router.subscriber_entitlements
WHERE subscriber_id = @subscriber_id::uuid
  AND status = 'active'
  AND billing_period_start <= clock_timestamp()
  AND clock_timestamp() < billing_period_end
FOR SHARE;

-- Serialize concurrent first admissions as well as existing bindings. Hash collisions only over-serialize.
-- name: GetServingConversationLock :exec
SELECT pg_advisory_xact_lock(hashtextextended(@admission_lock_key::text, 0));

-- Use database wall time after acquiring the admission lock, not transaction-start time.
-- name: GetServingAdmissionClock :one
SELECT clock_timestamp()::timestamptz AS admitted_at;

-- Exact scope lookup; never share a pseudo-session across all requests with no client ID.
-- name: GetSessionReleaseBinding :one
SELECT binding
FROM router.session_release_bindings
WHERE installation_id = @installation_id::uuid
  AND credential_scope = @credential_scope::varchar
  AND conversation_digest = @conversation_digest::bytea;

-- Called only while the installation/subject/key/conversation admission locks are held.
-- name: UpsertSessionReleaseBinding :execrows
INSERT INTO router.session_release_bindings (
    installation_id, credential_scope, conversation_digest, target, activation_id,
    release_sha256, binding_sha256, profile_key, profile_revision_sha256,
    enrollment_generation, assignment_generation, binding_generation,
    binding, created_at, last_admitted_at
) VALUES (
    @installation_id::uuid, @credential_scope::varchar, @conversation_digest::bytea,
    @target::varchar, @activation_id::uuid, @release_sha256::varchar, @binding_sha256::varchar,
    sqlc.narg(profile_key)::uuid, sqlc.narg(profile_revision_sha256)::varchar,
    @enrollment_generation::bigint, @assignment_generation::bigint, @binding_generation::bigint,
    @binding::jsonb, @created_at::timestamptz, @last_admitted_at::timestamptz
)
ON CONFLICT (installation_id, credential_scope, conversation_digest) DO UPDATE SET
    target = CASE WHEN router.session_release_bindings.binding_generation < EXCLUDED.binding_generation THEN EXCLUDED.target ELSE router.session_release_bindings.target END,
    activation_id = CASE WHEN router.session_release_bindings.binding_generation < EXCLUDED.binding_generation THEN EXCLUDED.activation_id ELSE router.session_release_bindings.activation_id END,
    release_sha256 = CASE WHEN router.session_release_bindings.binding_generation < EXCLUDED.binding_generation THEN EXCLUDED.release_sha256 ELSE router.session_release_bindings.release_sha256 END,
    binding_sha256 = CASE WHEN router.session_release_bindings.binding_generation < EXCLUDED.binding_generation THEN EXCLUDED.binding_sha256 ELSE router.session_release_bindings.binding_sha256 END,
    profile_key = CASE WHEN router.session_release_bindings.binding_generation < EXCLUDED.binding_generation THEN EXCLUDED.profile_key ELSE router.session_release_bindings.profile_key END,
    profile_revision_sha256 = CASE WHEN router.session_release_bindings.binding_generation < EXCLUDED.binding_generation THEN EXCLUDED.profile_revision_sha256 ELSE router.session_release_bindings.profile_revision_sha256 END,
    enrollment_generation = CASE WHEN router.session_release_bindings.binding_generation < EXCLUDED.binding_generation THEN EXCLUDED.enrollment_generation ELSE router.session_release_bindings.enrollment_generation END,
    assignment_generation = CASE WHEN router.session_release_bindings.binding_generation < EXCLUDED.binding_generation THEN EXCLUDED.assignment_generation ELSE router.session_release_bindings.assignment_generation END,
    binding_generation = CASE WHEN router.session_release_bindings.binding_generation < EXCLUDED.binding_generation THEN EXCLUDED.binding_generation ELSE router.session_release_bindings.binding_generation END,
    binding = CASE WHEN router.session_release_bindings.binding_generation < EXCLUDED.binding_generation THEN EXCLUDED.binding
        ELSE jsonb_set(router.session_release_bindings.binding, '{last_admitted_at}', EXCLUDED.binding->'last_admitted_at') END,
    last_admitted_at = EXCLUDED.last_admitted_at
WHERE router.session_release_bindings.binding_generation < EXCLUDED.binding_generation
   OR (
        router.session_release_bindings.binding_generation = EXCLUDED.binding_generation
        AND router.session_release_bindings.target = EXCLUDED.target
        AND router.session_release_bindings.activation_id = EXCLUDED.activation_id
        AND router.session_release_bindings.release_sha256 = EXCLUDED.release_sha256
        AND router.session_release_bindings.binding_sha256 = EXCLUDED.binding_sha256
        AND router.session_release_bindings.enrollment_generation = EXCLUDED.enrollment_generation
        AND router.session_release_bindings.assignment_generation = EXCLUDED.assignment_generation
        AND router.session_release_bindings.profile_key IS NOT DISTINCT FROM EXCLUDED.profile_key
    );
