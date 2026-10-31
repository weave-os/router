BEGIN;

-- Lets a model the user picked in their client (served as-is, not routed) bill
-- the caller's own Claude/ChatGPT subscription. Default false bills Weave
-- credits; routed requests keep following subscription_routing_disabled.
ALTER TABLE router.model_router_installations
  ADD COLUMN picked_model_subscription_enabled BOOLEAN NOT NULL DEFAULT FALSE;

COMMIT;
