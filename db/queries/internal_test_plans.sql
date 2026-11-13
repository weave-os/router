-- Eligible identities require their own enabled prepaid budget and an existing personal credential.
-- name: GetInternalTestIdentities :many
SELECT b.subject_id, b.installation_id, b.label, b.balance_usd_micros, s.enrollment_generation
FROM router.internal_test_budgets b
JOIN router.credential_subjects s ON s.id = b.subject_id
JOIN router.credential_subject_installations a ON a.subject_id = b.subject_id AND a.installation_id = b.installation_id
JOIN router.model_router_installations i ON i.id = b.installation_id
WHERE b.enabled AND b.balance_usd_micros > 0 AND s.internal_enrolled AND s.projection_complete
  AND s.revoked_at IS NULL AND a.access_enabled AND i.deleted_at IS NULL
  AND EXISTS (SELECT 1 FROM router.model_router_api_keys k WHERE k.installation_id = b.installation_id
      AND k.credential_subject_id = b.subject_id AND k.scope = 'routing' AND k.deleted_at IS NULL)
ORDER BY b.label, b.subject_id;

-- Read authoritative eligibility without a stale enrollment or balance cache.
-- name: GetInternalTestIdentity :one
SELECT b.subject_id, b.installation_id, b.label, b.balance_usd_micros, s.enrollment_generation
FROM router.internal_test_budgets b
JOIN router.credential_subjects s ON s.id = b.subject_id
JOIN router.credential_subject_installations a ON a.subject_id = b.subject_id AND a.installation_id = b.installation_id
JOIN router.model_router_installations i ON i.id = b.installation_id
WHERE b.subject_id = @subject_id::uuid AND b.enabled AND b.balance_usd_micros > 0
  AND s.internal_enrolled AND s.projection_complete AND s.revoked_at IS NULL AND a.access_enabled AND i.deleted_at IS NULL
  AND EXISTS (SELECT 1 FROM router.model_router_api_keys k WHERE k.installation_id = b.installation_id
      AND k.credential_subject_id = b.subject_id AND k.scope = 'routing' AND k.deleted_at IS NULL)
FOR SHARE OF b, s, a, i;

-- Persist only a digest of the bearer launch grant.
-- name: InsertInternalTestLaunch :exec
INSERT INTO router.internal_test_plan_launches(id, token_sha256, subject_id, installation_id, launch, created_at, expires_at)
VALUES (@id::uuid, @token_sha256::varchar, @subject_id::uuid, @installation_id::uuid, @launch::jsonb, @created_at::timestamptz, @expires_at::timestamptz);

-- A launch binds once to the CLI's fresh session and cannot be renewed.
-- name: UpdateInternalTestLaunchSession :one
UPDATE router.internal_test_plan_launches l SET session_id = @session_id::uuid
FROM router.model_router_api_keys k
WHERE l.token_sha256 = @token_sha256::varchar AND l.revoked_at IS NULL
  AND l.created_at <= clock_timestamp() AND clock_timestamp() < l.expires_at
  AND l.installation_id = @installation_id::uuid AND k.id = @api_key_id::uuid
  AND k.installation_id = l.installation_id AND k.credential_subject_id = l.subject_id
  AND k.scope = 'routing' AND k.deleted_at IS NULL
  AND (l.session_id IS NULL OR l.session_id = @session_id::uuid)
RETURNING l.launch;

-- Revocation changes only ephemeral internal grant state.
-- name: UpdateInternalTestLaunchRevoked :execrows
UPDATE router.internal_test_plan_launches SET revoked_at = COALESCE(revoked_at, clock_timestamp())
WHERE id = @id::uuid;

-- Read only the isolated book; no customer balance or override lookup.
-- name: GetInternalTestBalance :one
SELECT balance_usd_micros FROM router.internal_test_budgets
WHERE subject_id = @subject_id::uuid AND enabled;

-- Record every served action and meter the authenticating key atomically.
-- name: InsertInternalTestInferenceDebit :one
WITH charged AS (
 UPDATE router.internal_test_budgets SET balance_usd_micros = balance_usd_micros + @delta_usd_micros::bigint
 WHERE subject_id = @subject_id::uuid AND @delta_usd_micros::bigint <= 0
   AND balance_usd_micros + @delta_usd_micros::bigint >= 0
   AND EXISTS (SELECT 1 FROM router.model_router_api_keys k WHERE k.id = @api_key_id::uuid
       AND k.credential_subject_id = router.internal_test_budgets.subject_id
       AND k.installation_id = router.internal_test_budgets.installation_id)
 RETURNING subject_id, balance_usd_micros
), ledger AS (
 INSERT INTO router.internal_test_credit_ledger(subject_id, delta_usd_micros, router_request_id, router_model, api_key_id)
 SELECT subject_id, @delta_usd_micros::bigint, @router_request_id::varchar, @router_model::varchar, @api_key_id::uuid FROM charged
 RETURNING api_key_id
), metered AS (
 UPDATE router.model_router_api_keys SET spent_usd_micros = spent_usd_micros - @delta_usd_micros::bigint
 WHERE id IN (SELECT api_key_id FROM ledger)
)
SELECT balance_usd_micros FROM charged;
