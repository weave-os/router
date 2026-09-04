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
  updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- Account state is scoped by api_key_id so a router key can never manage another
-- user's subscription account.
-- name: ListModelRouterSubscriptionAccounts :many
SELECT *
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
    updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid AND api_key_id = @api_key_id::uuid;

-- name: DeleteModelRouterSubscriptionAccount :execrows
DELETE FROM router.model_router_subscription_accounts
WHERE id = @id::uuid AND api_key_id = @api_key_id::uuid;
