BEGIN;
ALTER TABLE router.model_router_subscription_accounts DROP COLUMN quota_reset_generation;
DROP TABLE router.codex_usage_resets;
COMMIT;
