BEGIN;
ALTER TABLE router.model_router_subscription_accounts
 ADD COLUMN token_refreshed_at TIMESTAMPTZ;
COMMIT;
