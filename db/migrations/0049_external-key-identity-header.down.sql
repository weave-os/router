BEGIN;

ALTER TABLE router.model_router_external_api_keys
  DROP CONSTRAINT model_router_external_api_keys_identity_header_check,
  DROP COLUMN identity_header_name,
  DROP COLUMN identity_header_format;

COMMIT;
