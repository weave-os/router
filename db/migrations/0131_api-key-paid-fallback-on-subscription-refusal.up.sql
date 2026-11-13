BEGIN;

-- A forced Claude turn whose every linked subscription refuses the client app
-- fails instead of billing paid API, unless an admin opts the key in here.
ALTER TABLE router.model_router_api_keys
  ADD COLUMN paid_fallback_on_subscription_refusal BOOLEAN NOT NULL DEFAULT FALSE;

COMMIT;
