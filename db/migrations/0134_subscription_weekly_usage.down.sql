BEGIN;

ALTER TABLE router.model_router_subscription_accounts
    DROP CONSTRAINT subscription_weekly_usage_valid,
    DROP COLUMN weekly_usage_fraction,
    DROP COLUMN weekly_usage_checked_at,
    DROP COLUMN weekly_usage_reset_at;

COMMIT;
