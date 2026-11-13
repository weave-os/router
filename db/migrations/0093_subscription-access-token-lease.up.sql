BEGIN;

ALTER TABLE router.model_router_subscription_accounts
  ADD COLUMN access_token_ciphertext BYTEA,
  ADD COLUMN access_token_expires_at TIMESTAMP,
  ADD COLUMN token_refresh_lease_until TIMESTAMP,
  ADD COLUMN token_refresh_lease_id UUID,
  ADD COLUMN token_refresh_version BIGINT NOT NULL DEFAULT 0;

COMMIT;
