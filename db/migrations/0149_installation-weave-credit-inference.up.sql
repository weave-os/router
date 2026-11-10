BEGIN;

-- Whether a turn no plan or provider key of the customer's can fund may run on
-- Weave's own provider keys and debit the organization's Weave credits.
-- Installations that already exist keep that fallback; new ones start without it.
ALTER TABLE router.model_router_installations
  ADD COLUMN weave_credit_inference_enabled BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE router.model_router_installations
   SET weave_credit_inference_enabled = TRUE;

COMMIT;
