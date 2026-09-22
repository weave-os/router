-- Enroll an account for a subscriber. The stable owner is the credential
-- subject, so a rotated or second harness key reaches the same row; api_key_id
-- records which key enrolled it. A legacy row still owned by the enrolling key
-- is adopted rather than duplicated, and the oldest one wins so concurrent
-- legacy duplicates from other keys are left untouched instead of merged. The
-- id tiebreak keeps that choice deterministic, so two keys adopting at once
-- converge on one row instead of racing for the subscriber-owned unique index.
-- name: UpsertModelRouterSubscriptionAccountForSubscriber :one
WITH owned AS (
  SELECT id, subscriber_id
  FROM router.model_router_subscription_accounts
  WHERE provider = @provider::varchar
    AND external_account_id = @external_account_id::varchar
    AND (subscriber_id = @subscriber_id::uuid
         OR (subscriber_id IS NULL AND api_key_id = @api_key_id::uuid))
  ORDER BY (subscriber_id IS NULL), created_at, id
  LIMIT 1
  FOR UPDATE
),
adopted AS (
  UPDATE router.model_router_subscription_accounts
  SET subscriber_id = @subscriber_id::uuid,
      refresh_token_ciphertext = @refresh_token_ciphertext::bytea,
      display_name = COALESCE(sqlc.narg('display_name')::text, display_name),
      enabled = TRUE,
      health_state = 'unknown',
      cooldown_until = NULL,
      access_token_ciphertext = NULL,
      access_token_expires_at = NULL,
      token_refresh_lease_until = NULL,
      token_refresh_lease_id = NULL,
      token_refresh_version = model_router_subscription_accounts.token_refresh_version + 1,
      updated_at = CURRENT_TIMESTAMP
  WHERE id = (SELECT id FROM owned)
  RETURNING id, subscriber_id, api_key_id, provider, external_account_id, display_name,
            refresh_token_ciphertext, enabled, health_state, cooldown_until, created_at
),
inserted AS (
  INSERT INTO router.model_router_subscription_accounts (
    subscriber_id, api_key_id, provider, external_account_id, refresh_token_ciphertext, display_name
  )
  SELECT @subscriber_id::uuid, @api_key_id::uuid, @provider::varchar,
         @external_account_id::varchar, @refresh_token_ciphertext::bytea,
         sqlc.narg('display_name')::text
  WHERE NOT EXISTS (SELECT 1 FROM owned)
  ON CONFLICT (subscriber_id, provider, external_account_id) WHERE subscriber_id IS NOT NULL
  DO UPDATE SET
    refresh_token_ciphertext = EXCLUDED.refresh_token_ciphertext,
    display_name = COALESCE(EXCLUDED.display_name, router.model_router_subscription_accounts.display_name),
    enabled = TRUE,
    health_state = 'unknown',
    cooldown_until = NULL,
    access_token_ciphertext = NULL,
    access_token_expires_at = NULL,
    token_refresh_lease_until = NULL,
    token_refresh_lease_id = NULL,
    token_refresh_version = model_router_subscription_accounts.token_refresh_version + 1,
    updated_at = CURRENT_TIMESTAMP
  RETURNING id, subscriber_id, api_key_id, provider, external_account_id, display_name,
            refresh_token_ciphertext, enabled, health_state, cooldown_until, created_at,
            (xmax = 0)::boolean AS inserted
)
SELECT id, subscriber_id, api_key_id, provider, external_account_id, display_name,
       refresh_token_ciphertext, enabled, health_state, cooldown_until, created_at,
       FALSE::boolean AS inserted,
       (SELECT subscriber_id IS NULL FROM owned)::boolean AS adopted
FROM adopted
UNION ALL
SELECT id, subscriber_id, api_key_id, provider, external_account_id, display_name,
       refresh_token_ciphertext, enabled, health_state, cooldown_until, created_at,
       inserted::boolean,
       FALSE::boolean AS adopted
FROM inserted;

-- Enroll an account for a key that has no credential subject. Such a row keeps
-- legacy api-key ownership until the subscriber reconnects it.
-- name: UpsertModelRouterSubscriptionAccount :one
INSERT INTO router.model_router_subscription_accounts (
  api_key_id, provider, external_account_id, refresh_token_ciphertext, display_name
)
VALUES (@api_key_id::uuid, @provider::varchar, @external_account_id::varchar, @refresh_token_ciphertext::bytea,
        sqlc.narg('display_name')::text)
ON CONFLICT (api_key_id, provider, external_account_id)
DO UPDATE SET
  refresh_token_ciphertext = EXCLUDED.refresh_token_ciphertext,
  display_name = COALESCE(EXCLUDED.display_name, router.model_router_subscription_accounts.display_name),
  enabled = TRUE,
  health_state = 'unknown',
  cooldown_until = NULL,
  access_token_ciphertext = NULL,
  access_token_expires_at = NULL,
  token_refresh_lease_until = NULL,
  token_refresh_lease_id = NULL,
  token_refresh_version = model_router_subscription_accounts.token_refresh_version + 1,
  updated_at = CURRENT_TIMESTAMP
RETURNING id, subscriber_id, api_key_id, provider, external_account_id, display_name,
          refresh_token_ciphertext, enabled, health_state, cooldown_until, created_at,
          (xmax = 0)::boolean AS inserted, FALSE::boolean AS adopted;

-- Account state is scoped by owner so a router key can never manage another
-- subscriber's account: subscriber-owned rows answer to the credential subject,
-- and rows left behind by the ownership migration answer only to the key that
-- enrolled them.
-- name: ListModelRouterSubscriptionAccounts :many
SELECT id,
       subscriber_id,
       api_key_id,
       provider,
       external_account_id,
       display_name,
       refresh_token_ciphertext,
       enabled,
       health_state,
       cooldown_until,
       created_at,
       updated_at
FROM router.model_router_subscription_accounts
WHERE subscriber_id = sqlc.narg(subscriber_id)::uuid
   OR (subscriber_id IS NULL AND api_key_id = sqlc.narg(api_key_id)::uuid)
ORDER BY provider, created_at;

-- name: UpdateModelRouterSubscriptionAccountState :execrows
UPDATE router.model_router_subscription_accounts
SET enabled = @enabled::boolean,
    health_state = CASE WHEN @enabled::boolean THEN 'unknown' ELSE 'disabled' END,
    cooldown_until = @cooldown_until::timestamp,
    updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid
  AND (subscriber_id = sqlc.narg(subscriber_id)::uuid
       OR (subscriber_id IS NULL AND api_key_id = sqlc.narg(api_key_id)::uuid));

-- A stale replica must not turn an operator-disabled account back on while
-- persisting a quota cooldown.
-- name: UpdateModelRouterSubscriptionAccountCooldown :execrows
UPDATE router.model_router_subscription_accounts
SET cooldown_until = @cooldown_until::timestamp,
    health_state = 'cooldown',
    updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid
  AND (subscriber_id = sqlc.narg(subscriber_id)::uuid
       OR (subscriber_id IS NULL AND api_key_id = sqlc.narg(api_key_id)::uuid))
  AND enabled = TRUE;

-- Updates the internal credential-free routing health for one linked account.
-- enabled = enabled AND @enabled never re-enables an operator-disabled row
-- (Activate/Exhaust) while still allowing ReconnectRequired to disable it.
-- name: UpdateModelRouterSubscriptionAccountHealth :execrows
UPDATE router.model_router_subscription_accounts
SET health_state = CASE
        WHEN @health_state::varchar = 'active'
             AND cooldown_until > CURRENT_TIMESTAMP
        THEN health_state
        ELSE @health_state::varchar
    END,
    enabled = enabled AND @enabled::boolean,
    cooldown_until = CASE
        WHEN @health_state::varchar = 'active'
             AND cooldown_until > CURRENT_TIMESTAMP
        THEN cooldown_until
        ELSE @cooldown_until::timestamp
    END,
    updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid
  AND (subscriber_id = sqlc.narg(subscriber_id)::uuid
       OR (subscriber_id IS NULL AND api_key_id = sqlc.narg(api_key_id)::uuid));

-- name: UpdateModelRouterSubscriptionRefreshToken :execrows
UPDATE router.model_router_subscription_accounts
SET refresh_token_ciphertext = @refresh_token_ciphertext::bytea,
    health_state = 'unknown',
    access_token_ciphertext = NULL,
    access_token_expires_at = NULL,
    token_refresh_lease_until = NULL,
    token_refresh_lease_id = NULL,
    token_refresh_version = token_refresh_version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid
  AND (subscriber_id = sqlc.narg(subscriber_id)::uuid
       OR (subscriber_id IS NULL AND api_key_id = sqlc.narg(api_key_id)::uuid));

-- name: DeleteModelRouterSubscriptionAccount :execrows
DELETE FROM router.model_router_subscription_accounts
WHERE id = @id::uuid
  AND (subscriber_id = sqlc.narg(subscriber_id)::uuid
       OR (subscriber_id IS NULL AND api_key_id = sqlc.narg(api_key_id)::uuid));

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
  WHERE id = @id::uuid
    AND (subscriber_id = sqlc.narg(subscriber_id)::uuid
         OR (subscriber_id IS NULL AND api_key_id = sqlc.narg(api_key_id)::uuid))
),
acquired AS (
  UPDATE router.model_router_subscription_accounts
  SET token_refresh_lease_until = CURRENT_TIMESTAMP + make_interval(secs => @lease_seconds::bigint),
      token_refresh_lease_id = @lease_id::uuid,
      updated_at = CURRENT_TIMESTAMP
  WHERE id = @id::uuid
    AND (subscriber_id = sqlc.narg(subscriber_id)::uuid
         OR (subscriber_id IS NULL AND api_key_id = sqlc.narg(api_key_id)::uuid))
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
  AND (subscriber_id = sqlc.narg(subscriber_id)::uuid
       OR (subscriber_id IS NULL AND api_key_id = sqlc.narg(api_key_id)::uuid))
  AND enabled = TRUE
  AND token_refresh_lease_id = @lease_id::uuid;

-- Release a refresh lease only when this holder still owns it.
-- name: ReleaseModelRouterSubscriptionRefreshLease :execrows
UPDATE router.model_router_subscription_accounts
SET token_refresh_lease_until = NULL,
    token_refresh_lease_id = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid
  AND (subscriber_id = sqlc.narg(subscriber_id)::uuid
       OR (subscriber_id IS NULL AND api_key_id = sqlc.narg(api_key_id)::uuid))
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
       health_state,
       cooldown_until
FROM router.model_router_subscription_accounts
WHERE id = @id::uuid
  AND (subscriber_id = sqlc.narg(subscriber_id)::uuid
       OR (subscriber_id IS NULL AND api_key_id = sqlc.narg(api_key_id)::uuid));

-- Persist a refresh result and publish its access token atomically. The lease
-- ID fences stale refreshers and the version prevents lost refresh rotations.
-- Quota health and cooldown are left untouched: a successful refresh only
-- proves credentials still work.
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
  AND (subscriber_id = sqlc.narg(subscriber_id)::uuid
       OR (subscriber_id IS NULL AND api_key_id = sqlc.narg(api_key_id)::uuid))
  AND enabled = TRUE
  AND token_refresh_lease_id = @lease_id::uuid
  AND token_refresh_version = @expected_version::bigint;

-- A failed refresher may disable only the credentials it still owns. Clearing
-- the lease in this update avoids a takeover between release and disable.
-- name: DisableModelRouterSubscriptionAccountIfRefreshHolder :execrows
UPDATE router.model_router_subscription_accounts
SET enabled = FALSE,
    health_state = 'reconnect_required',
    cooldown_until = NULL,
    token_refresh_lease_until = NULL,
    token_refresh_lease_id = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid
  AND (subscriber_id = sqlc.narg(subscriber_id)::uuid
       OR (subscriber_id IS NULL AND api_key_id = sqlc.narg(api_key_id)::uuid))
  AND enabled = TRUE
  AND token_refresh_lease_id = @lease_id::uuid
  AND token_refresh_version = @expected_version::bigint;

-- A stale refresh failure must not put the winner's credentials on cooldown.
-- name: CooldownModelRouterSubscriptionAccountIfRefreshHolder :execrows
UPDATE router.model_router_subscription_accounts
SET cooldown_until = @cooldown_until::timestamp,
    health_state = 'cooldown',
    token_refresh_lease_until = NULL,
    token_refresh_lease_id = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid
  AND (subscriber_id = sqlc.narg(subscriber_id)::uuid
       OR (subscriber_id IS NULL AND api_key_id = sqlc.narg(api_key_id)::uuid))
  AND enabled = TRUE
  AND token_refresh_lease_id = @lease_id::uuid
  AND token_refresh_version = @expected_version::bigint;
