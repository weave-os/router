BEGIN;
ALTER TABLE router.model_router_subscription_accounts
 DROP COLUMN token_refreshed_at;
COMMIT;
