BEGIN;
ALTER TABLE router.model_router_subscription_accounts
 ADD COLUMN quota_reactivated_at TIMESTAMPTZ;
COMMIT;
