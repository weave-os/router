BEGIN;

ALTER TABLE router.model_router_external_api_keys
  DROP COLUMN forwarded_client_headers,
  DROP COLUMN baggage_header;

COMMIT;
