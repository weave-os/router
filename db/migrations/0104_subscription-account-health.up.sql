BEGIN;

ALTER TABLE router.model_router_subscription_accounts
    ADD COLUMN health_state VARCHAR NOT NULL DEFAULT 'unknown';

UPDATE router.model_router_subscription_accounts
SET health_state = CASE
    WHEN enabled = FALSE THEN 'disabled'
    WHEN cooldown_until > CURRENT_TIMESTAMP THEN 'cooldown'
    ELSE 'unknown'
END;

ALTER TABLE router.model_router_subscription_accounts
    ADD CONSTRAINT model_router_subscription_accounts_health_state_valid
    CHECK (health_state IN (
        'active',
        'exhausted',
        'cooldown',
        'reconnect_required',
        'disabled',
        'unknown'
    ));

COMMIT;
