BEGIN;

CREATE TABLE router.model_router_subscription_accounts (
  id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  api_key_id             UUID NOT NULL REFERENCES router.model_router_api_keys(id) ON DELETE CASCADE,
  provider               VARCHAR(32) NOT NULL,
  external_account_id    VARCHAR(255) NOT NULL,
  refresh_token_ciphertext BYTEA NOT NULL,
  enabled                BOOLEAN NOT NULL DEFAULT TRUE,
  cooldown_until         TIMESTAMP,
  created_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (api_key_id, provider, external_account_id),
  CHECK (provider IN ('claude', 'codex'))
);

CREATE INDEX model_router_subscription_accounts_api_key_idx
  ON router.model_router_subscription_accounts(api_key_id, provider);

COMMIT;
