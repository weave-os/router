BEGIN;

DROP INDEX router.model_router_subscription_accounts_subscriber_idx;

DROP INDEX router.model_router_subscription_accounts_subscriber_account_idx;

ALTER TABLE router.model_router_subscription_accounts
    DROP CONSTRAINT model_router_subscription_accounts_owner_present;

-- Subscriber-owned rows whose enrolling key was deleted have no pre-migration
-- representation; the reinstated NOT NULL cannot hold them.
DELETE FROM router.model_router_subscription_accounts WHERE api_key_id IS NULL;

ALTER TABLE router.model_router_subscription_accounts
    DROP CONSTRAINT model_router_subscription_accounts_api_key_id_fkey;

ALTER TABLE router.model_router_subscription_accounts
    ADD CONSTRAINT model_router_subscription_accounts_api_key_id_fkey
    FOREIGN KEY (api_key_id) REFERENCES router.model_router_api_keys(id) ON DELETE CASCADE;

ALTER TABLE router.model_router_subscription_accounts
    ALTER COLUMN api_key_id SET NOT NULL;

ALTER TABLE router.model_router_subscription_accounts
    DROP COLUMN subscriber_id;

COMMIT;
