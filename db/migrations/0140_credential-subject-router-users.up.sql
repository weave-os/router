BEGIN;

ALTER TABLE router.model_router_users ADD COLUMN credential_subject_id uuid;
ALTER TABLE router.model_router_users ADD CONSTRAINT model_router_users_subject_installation_fk
    FOREIGN KEY (credential_subject_id, installation_id)
    REFERENCES router.credential_subject_installations(subject_id, installation_id);

ALTER TABLE router.model_router_users DROP CONSTRAINT model_router_users_identity_present;
ALTER TABLE router.model_router_users ADD CONSTRAINT model_router_users_identity_present
    CHECK (email IS NOT NULL OR claude_account_uuid IS NOT NULL OR credential_subject_id IS NOT NULL);

CREATE UNIQUE INDEX model_router_users_installation_subject_unique
    ON router.model_router_users(installation_id, credential_subject_id)
    WHERE email IS NULL
      AND claude_account_uuid IS NULL
      AND credential_subject_id IS NOT NULL
      AND deleted_at IS NULL;

COMMENT ON COLUMN router.model_router_users.credential_subject_id IS
    'Authenticated owner of the personal API key that carried a request with no client-asserted identity. Set only on rows created for such requests (email and claude_account_uuid NULL).';

COMMIT;
