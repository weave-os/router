BEGIN;
ALTER TABLE router.model_router_subscription_accounts
 DROP COLUMN quota_reactivated_at;
COMMIT;
