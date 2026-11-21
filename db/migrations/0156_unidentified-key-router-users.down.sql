BEGIN;

DELETE FROM router.model_router_users WHERE unidentified_api_key_id IS NOT NULL;
DROP INDEX router.model_router_users_installation_unidentified_key_unique;
ALTER TABLE router.model_router_users DROP CONSTRAINT model_router_users_identity_present;
ALTER TABLE router.model_router_users ADD CONSTRAINT model_router_users_identity_present
    CHECK (email IS NOT NULL OR claude_account_uuid IS NOT NULL OR credential_subject_id IS NOT NULL);
ALTER TABLE router.model_router_users DROP COLUMN unidentified_api_key_id;

COMMIT;
