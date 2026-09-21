BEGIN;

ALTER TABLE router.model_router_subscription_accounts
    DROP CONSTRAINT model_router_subscription_accounts_health_state_valid;

ALTER TABLE router.model_router_subscription_accounts
    DROP COLUMN health_state;

COMMIT;
