BEGIN;

ALTER TABLE router.model_router_users
    ADD COLUMN unidentified_api_key_id uuid REFERENCES router.model_router_api_keys(id) ON DELETE CASCADE;

ALTER TABLE router.model_router_users DROP CONSTRAINT model_router_users_identity_present;
ALTER TABLE router.model_router_users ADD CONSTRAINT model_router_users_identity_present
    CHECK (email IS NOT NULL OR claude_account_uuid IS NOT NULL OR credential_subject_id IS NOT NULL OR unidentified_api_key_id IS NOT NULL);

CREATE UNIQUE INDEX model_router_users_installation_unidentified_key_unique
    ON router.model_router_users(installation_id, unidentified_api_key_id)
    WHERE email IS NULL
      AND claude_account_uuid IS NULL
      AND credential_subject_id IS NULL
      AND unidentified_api_key_id IS NOT NULL
      AND deleted_at IS NULL;

COMMENT ON COLUMN router.model_router_users.unidentified_api_key_id IS
    'Ownerless (team) API key that carried a request with no client-asserted identity. One synthetic user per key, so per-user spend caps still apply to that traffic.';

COMMIT;
