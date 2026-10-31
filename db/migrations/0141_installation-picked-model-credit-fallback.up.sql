BEGIN;

-- A model the user picked in their client (served as-is, not routed) runs on
-- the caller's own Claude/ChatGPT login like the native client: its plan, then
-- the provider's own extra usage. Setting this lets such a pick fall back to
-- Weave credits once that login cannot serve it.
ALTER TABLE router.model_router_installations
  ADD COLUMN picked_model_credit_fallback_enabled BOOLEAN NOT NULL DEFAULT FALSE;

COMMIT;
