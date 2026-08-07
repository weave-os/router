BEGIN;

-- Egress fence: when non-empty, the installation's requests may only reach
-- these providers. Distinct from excluded_providers, which shapes routing but
-- which a fallback binding can still walk around — an empty array here means
-- "unfenced", and a non-empty one fails the request instead of falling back.
ALTER TABLE router.model_router_installations
  ADD COLUMN allowed_providers TEXT[] NOT NULL DEFAULT '{}';

COMMENT ON COLUMN router.model_router_installations.allowed_providers IS 'Egress fence: non-empty restricts this installation to these providers; empty means unfenced';

COMMIT;
