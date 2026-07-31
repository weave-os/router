BEGIN;

-- Managed-mode BYOK. Until now BYOK was reachable only from the self-hoster
-- dashboard: managed deploys strip customer provider keys in the auth
-- middleware because a stored key would spend upstream *and* debit prepaid
-- credits. These columns let a managed installation opt in explicitly, with
-- Weave charging a percentage fee on the customer's upstream spend instead of
-- the full inference cost.

-- Per-installation opt-in. Managed deploys honor BYOK rows only when this is
-- true, so one org enabling it can't change credentialing for every other
-- tenant on the deployment. Self-hosted ignores the flag (BYOK is the only
-- credentialing path there).
ALTER TABLE router.model_router_installations
  ADD COLUMN byok_enabled BOOLEAN NOT NULL DEFAULT FALSE;

-- Customer-supplied upstream endpoint for this key. NULL means "use the
-- deployment's configured base URL for the provider" -- the pre-existing
-- behavior. Non-NULL lets a customer point at their own deployment of an
-- OpenAI-compatible provider rather than the vendor's public endpoint.
ALTER TABLE router.model_router_external_api_keys
  ADD COLUMN base_url TEXT;

-- The original CHECK predates Bedrock, Makora, Together, and xAI becoming
-- first-class providers, so BYOK rows for them were rejected outright. Widen
-- it to the full providers.APIKeyEnvVars set.
ALTER TABLE router.model_router_external_api_keys
  DROP CONSTRAINT model_router_external_api_keys_provider_check;

ALTER TABLE router.model_router_external_api_keys
  ADD CONSTRAINT model_router_external_api_keys_provider_check
  CHECK (provider IN (
    'anthropic','openai','google','openrouter','fireworks',
    'bedrock','makora','together','xai'
  ));

-- BYOK turns write two ledger rows: an 'inference' row at delta 0 (the
-- customer paid their own provider) carrying notional_cost_micros for the
-- savings dashboard, plus a 'byok_fee' row holding the actual debit.
ALTER TABLE router.organization_credit_ledger
  DROP CONSTRAINT organization_credit_ledger_entry_type_check;

ALTER TABLE router.organization_credit_ledger
  ADD CONSTRAINT organization_credit_ledger_entry_type_check
  CHECK (entry_type IN ('topup','inference','refund','adjustment','byok_fee'));

COMMENT ON COLUMN router.model_router_installations.byok_enabled IS 'Managed-mode opt-in: honor this installation''s BYOK provider keys';
COMMENT ON COLUMN router.model_router_external_api_keys.base_url IS 'Customer-supplied upstream base URL; NULL uses the deployment default for the provider';

COMMIT;
