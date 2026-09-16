BEGIN;

ALTER TABLE router.model_router_subscription_accounts
  DROP COLUMN token_refresh_version,
  DROP COLUMN token_refresh_lease_id,
  DROP COLUMN token_refresh_lease_until,
  DROP COLUMN access_token_expires_at,
  DROP COLUMN access_token_ciphertext;

COMMIT;
