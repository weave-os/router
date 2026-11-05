BEGIN;

-- Rolling strike window for provider credential rejections, so one ambiguous
-- 401/400 cools an account down instead of forcing a reconnect.
ALTER TABLE router.model_router_subscription_accounts
    ADD COLUMN credential_rejection_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN credential_first_rejected_at TIMESTAMP,
    ADD COLUMN credential_last_rejection_reason VARCHAR(64);

COMMIT;
