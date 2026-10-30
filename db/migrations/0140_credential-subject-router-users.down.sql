BEGIN;

DELETE FROM router.model_router_users
WHERE email IS NULL AND claude_account_uuid IS NULL;

ALTER TABLE router.model_router_users DROP CONSTRAINT model_router_users_identity_present;
ALTER TABLE router.model_router_users ADD CONSTRAINT model_router_users_identity_present
    CHECK (email IS NOT NULL OR claude_account_uuid IS NOT NULL);

ALTER TABLE router.model_router_users DROP CONSTRAINT model_router_users_subject_installation_fk;
ALTER TABLE router.model_router_users DROP COLUMN credential_subject_id;

COMMIT;
