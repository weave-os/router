BEGIN;

-- An enterprise endpoint fronting a whole org typically authenticates the
-- service, not the person, and expects the calling user in a header of its own
-- choosing. Configured per key so identity only ever reaches the endpoint the
-- customer nominated. NULL keeps today's behavior: nothing is forwarded.
ALTER TABLE router.model_router_external_api_keys
  ADD COLUMN identity_header_name TEXT,
  ADD COLUMN identity_header_format TEXT
    CHECK (identity_header_format IN ('email','json')),
  ADD CONSTRAINT model_router_external_api_keys_identity_header_check
    CHECK ((identity_header_name IS NULL) = (identity_header_format IS NULL));

COMMENT ON COLUMN router.model_router_external_api_keys.identity_header_name IS 'Header carrying the caller identity to this key''s endpoint; NULL forwards nothing';
COMMENT ON COLUMN router.model_router_external_api_keys.identity_header_format IS 'email = bare address, json = URL-encoded JSON property bag';

COMMIT;
