BEGIN;

-- Narrowing the entry_type CHECK fails while byok_fee rows exist, so drop them
-- first. Destructive by necessity: the rolled-back schema has no legal
-- representation for a BYOK fee.
DELETE FROM router.organization_credit_ledger
WHERE entry_type = 'byok_fee';

ALTER TABLE router.organization_credit_ledger
  DROP CONSTRAINT organization_credit_ledger_entry_type_check;

ALTER TABLE router.organization_credit_ledger
  ADD CONSTRAINT organization_credit_ledger_entry_type_check
  CHECK (entry_type IN ('topup','inference','refund','adjustment'));

-- Same problem on the provider CHECK: rows for the four providers added by the
-- up migration can't satisfy the narrowed predicate.
DELETE FROM router.model_router_external_api_keys
WHERE provider IN ('bedrock','makora','together','xai');

ALTER TABLE router.model_router_external_api_keys
  DROP CONSTRAINT model_router_external_api_keys_provider_check;

ALTER TABLE router.model_router_external_api_keys
  ADD CONSTRAINT model_router_external_api_keys_provider_check
  CHECK (provider IN ('anthropic','openai','google','openrouter','fireworks'));

ALTER TABLE router.model_router_external_api_keys
  DROP COLUMN base_url;

ALTER TABLE router.model_router_installations
  DROP COLUMN byok_enabled;

COMMIT;
