BEGIN;

ALTER TABLE router.model_router_subscription_accounts
    ADD COLUMN weekly_usage_fraction DOUBLE PRECISION,
    ADD COLUMN weekly_usage_checked_at TIMESTAMPTZ,
    ADD COLUMN weekly_usage_reset_at TIMESTAMPTZ,
    ADD CONSTRAINT subscription_weekly_usage_valid CHECK (
        (weekly_usage_fraction IS NULL AND weekly_usage_checked_at IS NULL AND weekly_usage_reset_at IS NULL)
        OR (weekly_usage_fraction BETWEEN 0 AND 1 AND weekly_usage_fraction IS NOT NULL AND weekly_usage_checked_at IS NOT NULL)
    );

COMMIT;
