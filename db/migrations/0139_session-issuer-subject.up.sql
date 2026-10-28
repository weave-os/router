BEGIN;

ALTER TABLE router.model_router_api_keys
  DROP CONSTRAINT model_router_api_keys_personal_routing_only,
  ADD CONSTRAINT model_router_api_keys_personal_routing_only
    CHECK (credential_subject_id IS NULL OR scope IN ('routing', 'session_issuer'));

COMMIT;
