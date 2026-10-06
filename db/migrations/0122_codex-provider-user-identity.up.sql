BEGIN;

ALTER TABLE router.model_router_subscription_accounts ADD COLUMN provider_user_id TEXT;
ALTER TABLE router.model_router_subscription_accounts ADD CONSTRAINT subscription_provider_user_nonempty CHECK (provider_user_id IS NULL OR provider_user_id <> '');

-- Workspace-only credentials remain encrypted under their original binding.
-- Fresh enrollment verifies the provider user before restoring a legacy row.
UPDATE router.model_router_subscription_accounts
SET enabled = FALSE, health_state = 'reconnect_required'
WHERE provider = 'codex';

DROP INDEX router.model_router_subscription_accounts_active_physical_identity_idx;
CREATE UNIQUE INDEX model_router_subscription_accounts_active_physical_identity_idx
ON router.model_router_subscription_accounts(provider, external_account_id)
WHERE enabled AND provider <> 'codex';
CREATE UNIQUE INDEX model_router_subscription_accounts_codex_identity_idx
ON router.model_router_subscription_accounts(external_account_id, provider_user_id)
WHERE provider = 'codex' AND provider_user_id IS NOT NULL;

DROP INDEX router.model_router_subscription_accounts_subscriber_account_idx;
CREATE UNIQUE INDEX model_router_subscription_accounts_subscriber_account_idx
ON router.model_router_subscription_accounts(subscriber_id, provider, external_account_id)
WHERE subscriber_id IS NOT NULL AND provider <> 'codex';
CREATE UNIQUE INDEX model_router_subscription_accounts_codex_subscriber_idx
ON router.model_router_subscription_accounts(subscriber_id, external_account_id, provider_user_id)
WHERE subscriber_id IS NOT NULL AND provider = 'codex';

COMMIT;
