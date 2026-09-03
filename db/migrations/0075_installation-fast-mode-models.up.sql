BEGIN;

-- Org-wide fast-mode opt-in, written by the Weave control plane. Every
-- dispatch of a listed catalog model (routed, force-pinned, hard-pinned,
-- failover) is sent on the provider's fast tier -- OpenAI
-- service_tier=priority, Anthropic speed=fast -- and billed at that tier's
-- rate. Routing decisions keep scoring on list price. Empty array means no
-- model runs fast. Raw passthrough is unaffected.
ALTER TABLE router.model_router_installations
  ADD COLUMN fast_mode_models TEXT[] NOT NULL DEFAULT '{}';

COMMIT;
