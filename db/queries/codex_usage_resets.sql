-- Claim a subscriber's reset operation while preserving any uncertain redemption.
-- name: UpsertCodexResetLease :one
INSERT INTO router.codex_usage_resets (subscriber_id, lease_id, lease_until)
VALUES (@subscriber_id::uuid, @lease_id::uuid, CURRENT_TIMESTAMP + make_interval(secs => @lease_seconds::bigint))
ON CONFLICT (subscriber_id) DO UPDATE
SET lease_id = EXCLUDED.lease_id, lease_until = EXCLUDED.lease_until
WHERE codex_usage_resets.lease_until IS NULL OR codex_usage_resets.lease_until <= CURRENT_TIMESTAMP
RETURNING account_id, credit_id, request_id;

-- Persist a specific personal credit before making an irreversible provider call.
-- name: UpdateCodexResetSelection :execrows
UPDATE router.codex_usage_resets AS claim
SET account_id = @account_id::uuid, credit_id = @credit_id::text, request_id = @request_id::uuid
WHERE claim.subscriber_id = @subscriber_id::uuid AND claim.lease_id = @lease_id::uuid
  AND claim.lease_until > CURRENT_TIMESTAMP AND claim.credit_id IS NULL
  AND EXISTS (
    SELECT 1 FROM router.model_router_subscription_accounts AS account
    WHERE account.id = @account_id::uuid AND account.subscriber_id = claim.subscriber_id
      AND account.provider = 'codex' AND account.enabled
      AND account.health_state IN ('active', 'unknown', 'exhausted', 'cooldown')
  );

-- Publish verified headroom only while the current claim and account remain valid.
-- A different naturally recovered account does not resolve a pending redemption.
-- name: UpdateCodexResetRecovery :execrows
WITH held AS MATERIALIZED (
  SELECT subscriber_id
  FROM router.codex_usage_resets
  WHERE subscriber_id = @subscriber_id::uuid AND lease_id = @lease_id::uuid
    AND lease_until > CURRENT_TIMESTAMP
  FOR UPDATE
), recovered AS (
  UPDATE router.model_router_subscription_accounts AS account
  SET health_state = 'active', cooldown_until = NULL, updated_at = CURRENT_TIMESTAMP
  FROM held
  WHERE account.id = @account_id::uuid AND account.subscriber_id = @subscriber_id::uuid
    AND account.provider = 'codex' AND account.enabled
    AND account.health_state IN ('active', 'unknown', 'exhausted', 'cooldown')
    AND held.subscriber_id = account.subscriber_id
  RETURNING account.id
)
UPDATE router.codex_usage_resets AS claim
SET account_id = CASE WHEN claim.account_id = @account_id::uuid THEN NULL ELSE claim.account_id END,
    credit_id = CASE WHEN claim.account_id = @account_id::uuid THEN NULL ELSE claim.credit_id END,
    request_id = CASE WHEN claim.account_id = @account_id::uuid THEN NULL ELSE claim.request_id END
WHERE claim.subscriber_id = @subscriber_id::uuid AND claim.lease_id = @lease_id::uuid
  AND EXISTS (SELECT 1 FROM recovered);

-- Clear only a definitively rejected credit; ambiguous attempts remain reserved.
-- name: UpdateCodexResetClear :execrows
UPDATE router.codex_usage_resets
SET account_id = NULL, credit_id = NULL, request_id = NULL
WHERE subscriber_id = @subscriber_id::uuid AND lease_id = @lease_id::uuid
  AND lease_until > CURRENT_TIMESTAMP;

-- Release this operation without discarding a pending provider result.
-- name: UpdateCodexResetRelease :execrows
UPDATE router.codex_usage_resets
SET lease_id = NULL, lease_until = NULL
WHERE subscriber_id = @subscriber_id::uuid AND lease_id = @lease_id::uuid;
