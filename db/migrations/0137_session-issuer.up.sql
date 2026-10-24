BEGIN;

-- A session issuer is a factory-held Ed25519 public key registered per
-- installation. It authenticates nothing as a bearer: the router verifies
-- short-lived rs_ JWTs signed by the matching private key and attributes them
-- to this row. Revoking the row revokes every token it signed.
ALTER TABLE router.model_router_api_keys
  DROP CONSTRAINT model_router_api_keys_scope_check,
  ADD CONSTRAINT model_router_api_keys_scope_check
    CHECK (scope IN ('routing', 'analytics_read', 'session_issuer')),
  ADD COLUMN public_jwk JSONB,
  ADD CONSTRAINT model_router_api_keys_session_issuer_jwk
    CHECK ((scope = 'session_issuer') = (public_jwk IS NOT NULL));

COMMENT ON COLUMN router.model_router_api_keys.scope IS 'routing = rk_ data-plane key (can proxy and spend); analytics_read = ra_ export key (read-only, non-billable); session_issuer = public key that signs rs_ session JWTs (never a bearer)';
COMMENT ON COLUMN router.model_router_api_keys.public_jwk IS 'Ed25519 public key (RFC 8037 OKP JWK: kty, crv, x) of a session_issuer row; NULL for every other scope';

COMMIT;
