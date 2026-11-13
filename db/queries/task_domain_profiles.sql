-- Reserve a root before acquiring its cross-replica inference lock. An expired row, or a
-- failure whose retry window has passed, is reset for a fresh classification.
-- name: InsertTaskDomainProfile :exec
INSERT INTO router.task_domain_profiles (conversation_key, root_sha256, release_sha256, evidence_sha256)
VALUES (@conversation_key::text, @root_sha256::text, @release_sha256::text, @evidence_sha256::text)
ON CONFLICT (conversation_key, root_sha256, release_sha256, evidence_sha256)
DO UPDATE SET outcome = NULL, retry_after = NULL,
    expires_at = CASE WHEN task_domain_profiles.expires_at <= CURRENT_TIMESTAMP THEN CURRENT_TIMESTAMP + INTERVAL '30 days' ELSE task_domain_profiles.expires_at END
WHERE task_domain_profiles.expires_at <= CURRENT_TIMESTAMP
OR task_domain_profiles.retry_after <= CURRENT_TIMESTAMP;

-- Completed profiles never wait behind unrelated first-turn inference transactions.
-- A failure past its retry window is a miss, so the caller reclassifies.
-- name: GetTaskDomainProfile :one
SELECT outcome FROM router.task_domain_profiles
WHERE conversation_key = @conversation_key::text AND root_sha256 = @root_sha256::text
AND release_sha256 = @release_sha256::text AND evidence_sha256 = @evidence_sha256::text
AND expires_at > CURRENT_TIMESTAMP AND outcome IS NOT NULL
AND (retry_after IS NULL OR retry_after > CURRENT_TIMESTAMP);

-- The row lock spans only the bounded first classification and content-free commit.
-- name: GetTaskDomainProfileForUpdate :one
SELECT outcome FROM router.task_domain_profiles
WHERE conversation_key = @conversation_key::text AND root_sha256 = @root_sha256::text
AND release_sha256 = @release_sha256::text AND evidence_sha256 = @evidence_sha256::text
AND expires_at > CURRENT_TIMESTAMP
FOR UPDATE;

-- Persist outcomes without prompt text. A failed classification becomes retryable after
-- five minutes, at most once per window, instead of keeping the task on baseline ranking
-- for the 30-day profile lifetime. The row itself keeps that lifetime so it still counts
-- toward resume ambiguity.
-- name: UpdateTaskDomainProfile :exec
UPDATE router.task_domain_profiles SET outcome = @outcome::jsonb,
    retry_after = CASE WHEN @failed::boolean THEN CURRENT_TIMESTAMP + INTERVAL '5 minutes' END
WHERE conversation_key = @conversation_key::text AND root_sha256 = @root_sha256::text
AND release_sha256 = @release_sha256::text AND evidence_sha256 = @evidence_sha256::text;

-- A compacted conversation may recover only one unambiguous task root.
-- name: GetTaskDomainResumeProfiles :many
SELECT outcome FROM router.task_domain_profiles
WHERE conversation_key = @conversation_key::text AND release_sha256 = @release_sha256::text
AND evidence_sha256 = @evidence_sha256::text AND expires_at > CURRENT_TIMESTAMP
LIMIT 2;

-- Expiry never grants permission to classify a compaction summary as an original task.
-- name: DeleteExpiredTaskDomainProfiles :exec
DELETE FROM router.task_domain_profiles WHERE expires_at <= CURRENT_TIMESTAMP;
