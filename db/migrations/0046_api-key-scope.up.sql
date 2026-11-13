BEGIN;

-- Scope splits the credential in two. 'routing' is today's rk_ data-plane key:
-- it proxies inference and can therefore spend money. 'analytics_read' is a
-- read-only ra_ key for the analytics export — it authenticates nothing but
-- the export surface, so an ETL job in a customer's warehouse can never route
-- a request or draw down a spend cap even if the credential leaks.
ALTER TABLE router.model_router_api_keys
  ADD COLUMN scope VARCHAR NOT NULL DEFAULT 'routing'
  CHECK (scope IN ('routing', 'analytics_read'));

COMMENT ON COLUMN router.model_router_api_keys.scope IS 'routing = rk_ data-plane key (can proxy and spend); analytics_read = ra_ export key (read-only, non-billable)';

COMMIT;
