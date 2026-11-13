BEGIN;
ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN final_model_family,
    DROP COLUMN intended_model_family,
    DROP COLUMN subscription_tier,
    DROP COLUMN subscription_owner_id,
    DROP COLUMN subscription_account_id;
DROP INDEX router.model_router_subscription_accounts_active_physical_identity_idx;
-- The old schema rejects new ownerless rows. Keep existing legacy rows inert
-- without deleting their encrypted credentials or requiring a reassignment.
ALTER TABLE router.model_router_subscription_accounts
ADD CONSTRAINT model_router_subscription_accounts_owner_present
CHECK (subscriber_id IS NOT NULL OR api_key_id IS NOT NULL) NOT VALID;
DROP TABLE router.model_router_subscription_account_installations;
ALTER TABLE router.model_router_installations DROP COLUMN subscription_sharing_enabled;
COMMIT;
