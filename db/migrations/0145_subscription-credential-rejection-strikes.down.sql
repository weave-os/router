BEGIN;

ALTER TABLE router.model_router_subscription_accounts
    DROP COLUMN credential_last_rejection_reason,
    DROP COLUMN credential_first_rejected_at,
    DROP COLUMN credential_rejection_count;

COMMIT;
