-- name: UpsertModelRouterSubscriptionAccount :one
INSERT INTO router.model_router_subscription_accounts (
  api_key_id, provider, external_account_id, refresh_token_ciphertext
)
VALUES (@api_key_id::uuid, @provider::varchar, @external_account_id::varchar, @refresh_token_ciphertext::bytea)
ON CONFLICT (api_key_id, provider, external_account_id)
DO UPDATE SET
  refresh_token_ciphertext = EXCLUDED.refresh_token_ciphertext,
  enabled = TRUE,
  cooldown_until = NULL,
  access_token_ciphertext = NULL,
  access_token_expires_at = NULL,
  token_refresh_lease_until = NULL,
  token_refresh_lease_id = NULL,
  token_refresh_version = model_router_subscription_accounts.token_refresh_version + 1,
  updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- Account state is scoped by api_key_id so a router key can never manage another
-- user's subscription account.
-- name: ListModelRouterSubscriptionAccounts :many
SELECT id,
       api_key_id,
       provider,
       external_account_id,
       refresh_token_ciphertext,
       enabled,
       cooldown_until,
       created_at,
       updated_at
FROM router.model_router_subscription_accounts
WHERE api_key_id = @api_key_id::uuid
ORDER BY provider, created_at;

-- name: UpdateModelRouterSubscriptionAccountState :execrows
UPDATE router.model_router_subscription_accounts
SET enabled = @enabled::boolean,
    cooldown_until = @cooldown_until::timestamp,
    updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid AND api_key_id = @api_key_id::uuid;

-- A stale replica must not turn an operator-disabled account back on while
-- persisting a quota cooldown.
-- name: UpdateModelRouterSubscriptionAccountCooldown :execrows
UPDATE router.model_router_subscription_accounts
SET cooldown_until = @cooldown_until::timestamp,
    updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid
  AND api_key_id = @api_key_id::uuid
  AND enabled = TRUE;

-- name: UpdateModelRouterSubscriptionRefreshToken :execrows
UPDATE router.model_router_subscription_accounts
SET refresh_token_ciphertext = @refresh_token_ciphertext::bytea,
    access_token_ciphertext = NULL,
    access_token_expires_at = NULL,
    token_refresh_lease_until = NULL,
    token_refresh_lease_id = NULL,
    token_refresh_version = token_refresh_version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid AND api_key_id = @api_key_id::uuid;

-- name: DeleteModelRouterSubscriptionAccount :execrows
DELETE FROM router.model_router_subscription_accounts
WHERE id = @id::uuid AND api_key_id = @api_key_id::uuid;

-- Try to reserve one account for a cross-replica token refresh. The lease ID
-- fences stale holders after a lease expires and is taken over. took_over
-- reports whether an expired lease from a holder that never released was
-- replaced: that holder may already have spent the refresh token, so a
-- terminal provider error on a taken-over lease is not proof the account is
-- dead. Zero rows means the account is unavailable or still leased. Both
-- CTEs see the same snapshot, and the UPDATE's row lock re-checks the lease
-- predicate for concurrent acquirers, so no explicit FOR UPDATE is needed
-- (Postgres rejects locking a row the same statement modifies).
-- name: TryAcquireModelRouterSubscriptionRefreshLease :one
WITH prior AS (
  SELECT token_refresh_lease_id IS NOT NULL AS took_over
  FROM router.model_router_subscription_accounts
  WHERE id = @id::uuid AND api_key_id = @api_key_id::uuid
),
acquired AS (
  UPDATE router.model_router_subscription_accounts
  SET token_refresh_lease_until = CURRENT_TIMESTAMP + make_interval(secs => @lease_seconds::bigint),
      token_refresh_lease_id = @lease_id::uuid,
      updated_at = CURRENT_TIMESTAMP
  WHERE id = @id::uuid
    AND api_key_id = @api_key_id::uuid
    AND enabled = TRUE
    AND (cooldown_until IS NULL OR cooldown_until <= CURRENT_TIMESTAMP)
    AND (token_refresh_lease_until IS NULL OR token_refresh_lease_until <= CURRENT_TIMESTAMP)
  RETURNING id
)
SELECT prior.took_over::boolean AS took_over
FROM acquired
JOIN prior ON TRUE;

-- Extend a refresh lease while the holder's provider call is still in flight.
-- Zero rows means the lease was lost (taken over or reset), so the holder must
-- abandon its refresh.
-- name: ExtendModelRouterSubscriptionRefreshLease :execrows
UPDATE router.model_router_subscription_accounts
SET token_refresh_lease_until = CURRENT_TIMESTAMP + make_interval(secs => @lease_seconds::bigint),
    updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid
  AND api_key_id = @api_key_id::uuid
  AND enabled = TRUE
  AND token_refresh_lease_id = @lease_id::uuid;

-- Release a refresh lease only when this holder still owns it.
-- name: ReleaseModelRouterSubscriptionRefreshLease :execrows
UPDATE router.model_router_subscription_accounts
SET token_refresh_lease_until = NULL,
    token_refresh_lease_id = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid
  AND api_key_id = @api_key_id::uuid
  AND token_refresh_lease_id = @lease_id::uuid;

-- Load encrypted credentials and the refresh version used for optimistic CAS.
-- The auth service decrypts the ciphertexts before returning them to Runtime.
-- name: GetModelRouterSubscriptionCredentialRecord :one
SELECT external_account_id,
       provider,
       refresh_token_ciphertext,
       access_token_ciphertext,
       access_token_expires_at,
       token_refresh_version,
       token_refresh_lease_id,
       enabled,
       cooldown_until
FROM router.model_router_subscription_accounts
WHERE id = @id::uuid AND api_key_id = @api_key_id::uuid;

-- Persist a refresh result and publish its access token atomically. The lease
-- ID fences stale refreshers and the version prevents lost refresh rotations.
-- name: PersistModelRouterSubscriptionTokens :execrows
UPDATE router.model_router_subscription_accounts
SET refresh_token_ciphertext = @refresh_token_ciphertext::bytea,
    access_token_ciphertext = @access_token_ciphertext::bytea,
    access_token_expires_at = @access_token_expires_at::timestamp,
    token_refresh_lease_until = NULL,
    token_refresh_lease_id = NULL,
    token_refresh_version = token_refresh_version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid
  AND api_key_id = @api_key_id::uuid
  AND enabled = TRUE
  AND token_refresh_lease_id = @lease_id::uuid
  AND token_refresh_version = @expected_version::bigint;

-- A failed refresher may disable only the credentials it still owns. Clearing
-- the lease in this update avoids a takeover between release and disable.
-- name: DisableModelRouterSubscriptionAccountIfRefreshHolder :execrows
UPDATE router.model_router_subscription_accounts
SET enabled = FALSE,
    cooldown_until = NULL,
    token_refresh_lease_until = NULL,
    token_refresh_lease_id = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid
  AND api_key_id = @api_key_id::uuid
  AND enabled = TRUE
  AND token_refresh_lease_id = @lease_id::uuid
  AND token_refresh_version = @expected_version::bigint;

-- A stale refresh failure must not put the winner's credentials on cooldown.
-- name: CooldownModelRouterSubscriptionAccountIfRefreshHolder :execrows
UPDATE router.model_router_subscription_accounts
SET cooldown_until = @cooldown_until::timestamp,
    token_refresh_lease_until = NULL,
    token_refresh_lease_id = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid
  AND api_key_id = @api_key_id::uuid
  AND enabled = TRUE
  AND token_refresh_lease_id = @lease_id::uuid
  AND token_refresh_version = @expected_version::bigint;
