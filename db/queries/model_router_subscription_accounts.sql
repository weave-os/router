-- Enrollment updates only the same verified owner. Codex seats are provider
-- users within a workspace; fresh verified enrollment may repair an owned
-- workspace-only legacy row but never adopts another owner or an unassigned row.
-- name: UpsertModelRouterSubscriptionAccountForSubscriber :one
WITH owned AS (
  SELECT id, subscriber_id
  FROM router.model_router_subscription_accounts
  WHERE provider = @provider::varchar
    AND external_account_id = @external_account_id::varchar
    AND EXISTS (SELECT 1 FROM router.credential_subjects AS subject
                JOIN router.credential_subject_installations AS access ON access.subject_id = subject.id
                JOIN router.model_router_installations AS installation ON installation.id = access.installation_id
                WHERE subject.id = @subscriber_id::uuid AND subject.projection_complete AND subject.revoked_at IS NULL
                  AND access.installation_id = @installation_id::uuid AND access.access_enabled AND installation.deleted_at IS NULL)
    AND subscriber_id = @subscriber_id::uuid
    AND (@provider::varchar <> 'codex' OR (NULLIF(@provider_user_id::text, '') IS NOT NULL AND (provider_user_id = @provider_user_id::text OR provider_user_id IS NULL)))
    AND NOT EXISTS (SELECT 1 FROM router.model_router_subscription_accounts AS other
                    WHERE other.provider = @provider::varchar AND other.external_account_id = @external_account_id::varchar
                      AND other.id <> model_router_subscription_accounts.id
                      AND (@provider::varchar <> 'codex' OR other.provider_user_id = @provider_user_id::text))
  ORDER BY (provider_user_id IS NULL), created_at, id
  LIMIT 1
  FOR UPDATE
),
adopted AS (
  UPDATE router.model_router_subscription_accounts
  SET subscriber_id = @subscriber_id::uuid,
      provider_user_id = NULLIF(@provider_user_id::text, ''),
      refresh_token_ciphertext = @refresh_token_ciphertext::bytea,
      display_name = COALESCE(sqlc.narg('display_name')::text, display_name),
      enabled = TRUE,
      health_state = 'unknown',
      cooldown_until = NULL,
      access_token_ciphertext = NULL,
      access_token_expires_at = NULL,
      credential_rejection_count = 0,
      credential_first_rejected_at = NULL,
      credential_last_rejection_reason = NULL,
      token_refresh_lease_until = NULL,
      token_refresh_lease_id = NULL,
      token_refresh_version = model_router_subscription_accounts.token_refresh_version + 1,
      updated_at = CURRENT_TIMESTAMP
  WHERE id = (SELECT id FROM owned)
  RETURNING id, subscriber_id, api_key_id, provider, external_account_id, provider_user_id, display_name,
            refresh_token_ciphertext, enabled, health_state, cooldown_until, created_at
),
inserted AS (
  INSERT INTO router.model_router_subscription_accounts (
    subscriber_id, api_key_id, provider, external_account_id, provider_user_id, refresh_token_ciphertext, display_name
  )
  SELECT @subscriber_id::uuid, @api_key_id::uuid, @provider::varchar,
         @external_account_id::varchar, NULLIF(@provider_user_id::text, ''), @refresh_token_ciphertext::bytea,
         sqlc.narg('display_name')::text
  WHERE (@provider::varchar <> 'codex' OR NULLIF(@provider_user_id::text, '') IS NOT NULL)
    AND NOT EXISTS (SELECT 1 FROM owned)
    AND NOT EXISTS (SELECT 1 FROM router.model_router_subscription_accounts AS physical
                    WHERE physical.provider = @provider::varchar AND physical.external_account_id = @external_account_id::varchar
                      AND (@provider::varchar <> 'codex' OR physical.provider_user_id = @provider_user_id::text))
    AND EXISTS (SELECT 1 FROM router.credential_subjects AS subject
                JOIN router.credential_subject_installations AS access ON access.subject_id = subject.id
                JOIN router.model_router_installations AS installation ON installation.id = access.installation_id
                WHERE subject.id = @subscriber_id::uuid AND subject.projection_complete AND subject.revoked_at IS NULL
                  AND access.installation_id = @installation_id::uuid AND access.access_enabled AND installation.deleted_at IS NULL)
  RETURNING id, subscriber_id, api_key_id, provider, external_account_id, provider_user_id, display_name,
            refresh_token_ciphertext, enabled, health_state, cooldown_until, created_at,
            TRUE::boolean AS inserted
),
registered AS (
  INSERT INTO router.model_router_subscription_account_installations (installation_id, subscription_account_id)
  SELECT @installation_id::uuid, id FROM adopted
  UNION ALL SELECT @installation_id::uuid, id FROM inserted
  ON CONFLICT DO NOTHING
)
SELECT id, subscriber_id, api_key_id, provider, external_account_id, provider_user_id, display_name,
       refresh_token_ciphertext, enabled, health_state, cooldown_until, created_at,
       FALSE::boolean AS inserted,
       (SELECT subscriber_id IS NULL FROM owned)::boolean AS adopted
FROM adopted
UNION ALL
SELECT id, subscriber_id, api_key_id, provider, external_account_id, provider_user_id, display_name,
       refresh_token_ciphertext, enabled, health_state, cooldown_until, created_at,
       inserted::boolean,
       FALSE::boolean AS adopted
FROM inserted;

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
       provider_user_id,
       display_name,
       refresh_token_ciphertext,
       enabled,
       health_state,
       cooldown_until,
       created_at,
       updated_at,
       credential_rejection_count
FROM router.model_router_subscription_accounts
WHERE subscriber_id = sqlc.narg(subscriber_id)::uuid
   OR (subscriber_id IS NULL AND api_key_id = sqlc.narg(api_key_id)::uuid)
ORDER BY provider, created_at;

-- A duplicate physical identity stays quarantined until the conflicting rows are reconciled.
-- name: UpdateModelRouterSubscriptionAccountState :execrows
UPDATE router.model_router_subscription_accounts AS account
SET enabled = @enabled::boolean,
    health_state = CASE WHEN @enabled::boolean THEN 'unknown' ELSE 'disabled' END,
    credential_rejection_count = CASE WHEN @enabled::boolean THEN 0 ELSE account.credential_rejection_count END,
    credential_first_rejected_at = CASE WHEN @enabled::boolean THEN NULL ELSE account.credential_first_rejected_at END,
    credential_last_rejection_reason = CASE WHEN @enabled::boolean THEN NULL ELSE account.credential_last_rejection_reason END,
    cooldown_until = @cooldown_until::timestamp,
    updated_at = CURRENT_TIMESTAMP
WHERE account.id = @id::uuid
  AND (account.subscriber_id = sqlc.narg(subscriber_id)::uuid
       OR (account.subscriber_id IS NULL AND account.api_key_id = sqlc.narg(api_key_id)::uuid))
  AND (
      NOT @enabled::boolean
      OR ((account.provider <> 'codex' OR account.provider_user_id IS NOT NULL) AND NOT EXISTS (
          SELECT 1
          FROM router.model_router_subscription_accounts AS conflicting
          WHERE conflicting.provider = account.provider
            AND conflicting.external_account_id = account.external_account_id
            AND conflicting.id <> account.id
            AND (account.provider <> 'codex' OR conflicting.provider_user_id = account.provider_user_id)
      ))
  );

-- A stale replica must not turn an operator-disabled account back on while
-- persisting a quota cooldown.
-- name: UpdateModelRouterSubscriptionAccountCooldown :execrows
UPDATE router.model_router_subscription_accounts AS account
SET cooldown_until = GREATEST(
        account.cooldown_until,
        @cooldown_until::timestamp
    ),
    health_state = 'cooldown',
    updated_at = CURRENT_TIMESTAMP
WHERE account.id = @id::uuid
  AND (subscriber_id = sqlc.narg(subscriber_id)::uuid
       OR (subscriber_id IS NULL AND api_key_id = sqlc.narg(api_key_id)::uuid))
  AND enabled = TRUE;

-- Updates the internal credential-free routing health for one linked account.
-- enabled = enabled AND @enabled never re-enables an operator-disabled row
-- (Activate/Exhaust) while still allowing ReconnectRequired to disable it.
-- name: UpdateModelRouterSubscriptionAccountHealth :one
WITH previous AS (
 SELECT id, health_state FROM router.model_router_subscription_accounts
 WHERE id = @id::uuid
 AND (subscriber_id = sqlc.narg(subscriber_id)::uuid
 OR (subscriber_id IS NULL AND api_key_id = sqlc.narg(api_key_id)::uuid))
 FOR UPDATE
), updated AS (
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
WHERE id IN (SELECT previous.id FROM previous)
RETURNING *
), recorded AS (
INSERT INTO router.subscription_credential_events
 (account_id, subscriber_id, provider, event_kind, reason, token_refresh_version,
  rejection_count, first_rejected_at, refresh_lease_id, provider_error_type,
  provider_request_id, upstream_status, request_id)
SELECT account.id, account.subscriber_id, account.provider, 'reconnect_required',
 COALESCE(account.credential_last_rejection_reason, 'credential_rejected'), account.token_refresh_version,
 account.credential_rejection_count, account.credential_first_rejected_at, NULL::uuid,
 COALESCE(rejection.provider_error_type, ''), COALESCE(rejection.provider_request_id, ''),
 COALESCE(rejection.upstream_status, 0), COALESCE(rejection.request_id, '')
FROM updated AS account
LEFT JOIN LATERAL (
 SELECT event.provider_error_type, event.provider_request_id, event.upstream_status, event.request_id
 FROM router.subscription_credential_events AS event
 WHERE event.account_id = account.id AND event.event_kind = 'credential_rejected'
  AND event.token_refresh_version = account.token_refresh_version
 ORDER BY event.created_at DESC, event.id DESC LIMIT 1
) AS rejection ON TRUE
WHERE account.health_state = 'reconnect_required'
 AND EXISTS (SELECT 1 FROM previous WHERE previous.id = account.id AND previous.health_state <> 'reconnect_required')
 RETURNING id
)
SELECT count(*)::bigint AS matched_count,
       (SELECT count(*) > 0 FROM recorded)::boolean AS disconnected
FROM updated;

-- name: UpdateModelRouterSubscriptionRefreshToken :execrows
UPDATE router.model_router_subscription_accounts
SET refresh_token_ciphertext = @refresh_token_ciphertext::bytea,
    health_state = 'unknown',
    credential_rejection_count = 0,
    credential_first_rejected_at = NULL,
    credential_last_rejection_reason = NULL,
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
    AND (cooldown_until IS NULL OR cooldown_until <= CURRENT_TIMESTAMP
         OR (@allow_claude_overage::boolean AND provider = 'claude' AND health_state = 'exhausted'))
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
       provider_user_id,
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
    credential_last_rejection_reason = NULL,
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
WITH updated AS (
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
  AND token_refresh_version = @expected_version::bigint
RETURNING *
)
INSERT INTO router.subscription_credential_events
 (account_id, subscriber_id, provider, event_kind, reason, token_refresh_version,
  rejection_count, first_rejected_at, refresh_lease_id, provider_error_type,
  provider_request_id, upstream_status, request_id)
SELECT account.id, account.subscriber_id, account.provider, 'reconnect_required',
 COALESCE(account.credential_last_rejection_reason, 'credential_rejected'), account.token_refresh_version,
 account.credential_rejection_count, account.credential_first_rejected_at, @lease_id::uuid,
 COALESCE(rejection.provider_error_type, ''), COALESCE(rejection.provider_request_id, ''),
 COALESCE(rejection.upstream_status, 0), COALESCE(rejection.request_id, '')
FROM updated AS account
LEFT JOIN LATERAL (
 SELECT event.provider_error_type, event.provider_request_id, event.upstream_status, event.request_id
 FROM router.subscription_credential_events AS event
 WHERE event.account_id = account.id AND event.event_kind = 'credential_rejected'
  AND event.token_refresh_version = account.token_refresh_version
 ORDER BY event.created_at DESC, event.id DESC LIMIT 1
) AS rejection ON TRUE
;

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

-- Read from the primary so admission observes current membership, key lifecycle and sharing settings.
-- Locks are held only for this statement and serialize with settings/access writes.
-- name: ListModelRouterSubscriptionCandidates :many
WITH installation AS MATERIALIZED (
  SELECT id, subscription_sharing_enabled
  FROM router.model_router_installations
  WHERE id = @installation_id::uuid AND deleted_at IS NULL AND NOT subscription_routing_disabled
  FOR SHARE
), members AS MATERIALIZED (
  SELECT access.subject_id
  FROM router.credential_subject_installations AS access
  JOIN router.credential_subjects AS subject ON subject.id = access.subject_id
  JOIN installation ON installation.id = access.installation_id
  WHERE access.access_enabled AND subject.projection_complete AND subject.revoked_at IS NULL
  FOR SHARE OF access, subject
)
SELECT account.id, account.subscriber_id, account.api_key_id, account.provider,
       account.external_account_id, account.provider_user_id, account.display_name, account.refresh_token_ciphertext,
       account.enabled, account.health_state, account.cooldown_until, account.created_at,
       CASE WHEN account.subscriber_id = sqlc.narg(subscriber_id)::uuid THEN 'personal' ELSE 'shared' END AS tier,
       (account.access_token_ciphertext IS NULL)::boolean AS access_token_missing,
       account.credential_rejection_count
FROM router.model_router_subscription_accounts AS account
JOIN members ON members.subject_id = account.subscriber_id
JOIN installation ON TRUE
WHERE (account.provider <> 'codex' OR account.provider_user_id IS NOT NULL)
 AND (account.subscriber_id = sqlc.narg(subscriber_id)::uuid
   OR (installation.subscription_sharing_enabled
       -- A personal key borrows only while its owner remains an active member;
       -- a live installation-shared (subject-less) key always may.
       AND (EXISTS (SELECT 1 FROM members AS requester WHERE requester.subject_id = sqlc.narg(subscriber_id)::uuid)
         OR (sqlc.narg(subscriber_id)::uuid IS NULL AND EXISTS (
           SELECT 1 FROM router.model_router_api_keys AS requester_key
           WHERE requester_key.id = sqlc.narg(api_key_id)::uuid
             AND requester_key.installation_id = installation.id
             AND requester_key.deleted_at IS NULL
             AND requester_key.credential_subject_id IS NULL)))
       AND EXISTS (
        SELECT 1 FROM router.model_router_subscription_account_installations AS registration
        WHERE registration.installation_id = installation.id AND registration.subscription_account_id = account.id)))
ORDER BY (account.subscriber_id = sqlc.narg(subscriber_id)::uuid) DESC NULLS LAST, account.created_at, account.id
FOR SHARE OF account;

-- Counts one provider credential rejection inside a rolling window. Strikes
-- older than the window restart at one. The previous reason is returned so
-- the caller can recognise back-to-back explicit invalid_grant rejections; a
-- successful refresh clears it. A refresh rejection passes its lease and
-- version so a refresher that lost the lease records nothing.
-- name: RecordModelRouterSubscriptionCredentialRejection :one
WITH previous AS (
  SELECT id, credential_last_rejection_reason
  FROM router.model_router_subscription_accounts
  WHERE id = @id::uuid
    AND (subscriber_id = sqlc.narg(subscriber_id)::uuid
         OR (subscriber_id IS NULL AND api_key_id = sqlc.narg(api_key_id)::uuid))
    AND enabled = TRUE
  AND (sqlc.narg(lease_id)::uuid IS NULL
       OR (token_refresh_lease_id = sqlc.narg(lease_id)::uuid
           AND token_refresh_version = sqlc.narg(expected_version)::bigint))
  FOR UPDATE
), updated AS (
UPDATE router.model_router_subscription_accounts AS account
SET credential_rejection_count = CASE
        WHEN account.credential_first_rejected_at IS NULL
             OR account.credential_first_rejected_at < @window_start::timestamp
        THEN 1
        ELSE account.credential_rejection_count + 1
    END,
    credential_first_rejected_at = CASE
        WHEN account.credential_first_rejected_at IS NULL
             OR account.credential_first_rejected_at < @window_start::timestamp
        THEN @rejected_at::timestamp
        ELSE account.credential_first_rejected_at
    END,
    credential_last_rejection_reason = @reason::varchar,
    access_token_ciphertext = CASE WHEN @invalidate_access_token::boolean THEN NULL ELSE account.access_token_ciphertext END,
    access_token_expires_at = CASE WHEN @invalidate_access_token::boolean THEN NULL ELSE account.access_token_expires_at END,
    updated_at = CURRENT_TIMESTAMP
FROM previous
WHERE account.id = previous.id
RETURNING account.*, previous.credential_last_rejection_reason AS previous_reason
), recorded AS (
 INSERT INTO router.subscription_credential_events
  (account_id, subscriber_id, provider, event_kind, reason, provider_error_type,
   provider_request_id, upstream_status, request_id, refresh_lease_id,
   token_refresh_version, rejection_count, first_rejected_at)
 SELECT id, subscriber_id, provider, 'credential_rejected', @reason::varchar,
  @provider_error_type::varchar, @provider_request_id::varchar, @upstream_status::integer,
  @request_id::varchar, sqlc.narg(lease_id)::uuid, token_refresh_version,
  credential_rejection_count, credential_first_rejected_at
 FROM updated
 RETURNING id
)
SELECT updated.credential_rejection_count, updated.credential_first_rejected_at, updated.previous_reason,
 EXISTS (
  SELECT 1 FROM router.subscription_credential_events AS prior_disconnect
  WHERE prior_disconnect.account_id = @id::uuid
   AND prior_disconnect.event_kind = 'reconnect_required'
   AND prior_disconnect.created_at >= CURRENT_TIMESTAMP - INTERVAL '24 hours'
   AND prior_disconnect.token_refresh_version <> updated.token_refresh_version
 )::boolean AS repeated_disconnect
FROM updated;

-- Browser reconnect has no request key, so it enrolls through the subscriber's
-- newest active personal routing key whose installation access is enabled.
-- name: GetSubscriberEnrollmentKey :one
SELECT k.id AS api_key_id, k.installation_id
FROM router.model_router_api_keys AS k
JOIN router.credential_subjects AS subject ON subject.id = k.credential_subject_id
JOIN router.credential_subject_installations AS access
  ON access.subject_id = subject.id AND access.installation_id = k.installation_id
JOIN router.model_router_installations AS installation ON installation.id = k.installation_id
WHERE k.credential_subject_id = @subscriber_id::uuid
  AND k.deleted_at IS NULL
  AND installation.deleted_at IS NULL
  AND subject.projection_complete
  AND subject.revoked_at IS NULL
  AND access.access_enabled
ORDER BY k.created_at DESC, k.id
LIMIT 1;
