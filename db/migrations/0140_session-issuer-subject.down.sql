BEGIN;

-- Remove the newly supported issuers, invalidating their tokens before restoring
-- the constraint. Leaving them unbound would change their billing identity.
DELETE FROM router.model_router_api_keys
WHERE scope = 'session_issuer' AND credential_subject_id IS NOT NULL;

ALTER TABLE router.model_router_api_keys
  DROP CONSTRAINT model_router_api_keys_personal_routing_only,
  ADD CONSTRAINT model_router_api_keys_personal_routing_only
    CHECK (credential_subject_id IS NULL OR scope = 'routing');

COMMIT;
