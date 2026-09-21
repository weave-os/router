BEGIN;

ALTER TABLE router.model_router_subscription_accounts
    ADD COLUMN display_name TEXT;

COMMENT ON COLUMN router.model_router_subscription_accounts.display_name IS
    'Provider-supplied human-readable account label; never used for identity or deduplication.';

COMMIT;
