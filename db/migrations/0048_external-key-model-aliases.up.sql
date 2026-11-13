BEGIN;

-- A BYOK endpoint can publish the same models under its own names (common for
-- enterprise gateways with an internal naming scheme). The map is catalog
-- model id -> the id that endpoint expects. Routing, pricing, and telemetry
-- keep using the catalog id; only the outbound wire name is rewritten.
ALTER TABLE router.model_router_external_api_keys
  ADD COLUMN model_aliases JSONB;

COMMENT ON COLUMN router.model_router_external_api_keys.model_aliases IS 'Catalog model id -> upstream model id rewrite for this key''s endpoint; NULL means no rewrite';

COMMIT;
