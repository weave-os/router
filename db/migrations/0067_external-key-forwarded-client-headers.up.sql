BEGIN;

-- Vendor gateways that front their own observability (Snowflake Cortex is the
-- first) need the caller's own request headers to survive the hop through the
-- router, plus the router-resolved user identity merged into their JSON baggage bag.
ALTER TABLE router.model_router_external_api_keys
  ADD COLUMN forwarded_client_headers TEXT[],
  ADD COLUMN baggage_header TEXT;

COMMENT ON COLUMN router.model_router_external_api_keys.forwarded_client_headers IS 'Inbound client header names copied verbatim to this key''s endpoint; NULL forwards nothing';
COMMENT ON COLUMN router.model_router_external_api_keys.baggage_header IS 'JSON baggage header forwarded to this key''s endpoint with the resolved caller email under on-behalf-of; NULL forwards nothing';

COMMIT;
