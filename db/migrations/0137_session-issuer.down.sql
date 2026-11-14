BEGIN;

-- Issuers have no representation in the rolled-back schema; leaving them would
-- turn a public-key row into an unscoped one. Their tokens stop verifying.
DELETE FROM router.model_router_api_keys
WHERE scope = 'session_issuer';

ALTER TABLE router.model_router_api_keys
  DROP CONSTRAINT model_router_api_keys_session_issuer_jwk,
  DROP COLUMN public_jwk,
  DROP CONSTRAINT model_router_api_keys_scope_check,
  ADD CONSTRAINT model_router_api_keys_scope_check
    CHECK (scope IN ('routing', 'analytics_read'));

COMMIT;
