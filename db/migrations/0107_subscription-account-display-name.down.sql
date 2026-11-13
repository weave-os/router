BEGIN;

ALTER TABLE router.model_router_subscription_accounts
    DROP COLUMN display_name;

COMMIT;
