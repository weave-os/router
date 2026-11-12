BEGIN;

-- Whether a provider key is spent before the caller's own subscription
-- (key_first) or only when no subscription can serve the turn (plan_first).
-- Existing Anthropic keys keep the key-first funding they were created under.
ALTER TABLE router.model_router_external_api_keys
  ADD COLUMN funding_priority TEXT NOT NULL DEFAULT 'plan_first'
    CHECK (funding_priority IN ('key_first', 'plan_first'));

UPDATE router.model_router_external_api_keys
   SET funding_priority = 'key_first'
 WHERE provider = 'anthropic';

COMMENT ON COLUMN router.model_router_external_api_keys.funding_priority IS 'key_first spends this key before any subscription for its provider; plan_first uses it only when no subscription can serve the turn';

COMMIT;
