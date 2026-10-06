BEGIN;

-- Old readers cannot distinguish seats. Do not silently choose or delete one.
-- Multiple seats for one subscriber/workspace require operator reconciliation
-- before rollback; restoring the old unique index otherwise fails atomically.
UPDATE router.model_router_subscription_accounts SET enabled = FALSE, health_state = 'disabled'
WHERE provider = 'codex';
DROP INDEX router.model_router_subscription_accounts_codex_identity_idx;
DROP INDEX router.model_router_subscription_accounts_codex_subscriber_idx;
DROP INDEX router.model_router_subscription_accounts_active_physical_identity_idx;
DROP INDEX router.model_router_subscription_accounts_subscriber_account_idx;
CREATE UNIQUE INDEX model_router_subscription_accounts_active_physical_identity_idx
ON router.model_router_subscription_accounts(provider, external_account_id) WHERE enabled;
CREATE UNIQUE INDEX model_router_subscription_accounts_subscriber_account_idx
ON router.model_router_subscription_accounts(subscriber_id, provider, external_account_id)
WHERE subscriber_id IS NOT NULL;
ALTER TABLE router.model_router_subscription_accounts DROP CONSTRAINT subscription_provider_user_nonempty;
ALTER TABLE router.model_router_subscription_accounts DROP COLUMN provider_user_id;

COMMIT;
