BEGIN;

CREATE TABLE router.credential_subjects (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    projection_complete boolean NOT NULL DEFAULT false,
    internal_enrolled boolean NOT NULL DEFAULT false,
    enrollment_generation bigint NOT NULL DEFAULT 0 CHECK (enrollment_generation >= 0),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    revoked_at timestamptz,
    CHECK (NOT internal_enrolled OR (projection_complete AND revoked_at IS NULL))
);

CREATE TABLE router.credential_subject_installations (
    subject_id uuid NOT NULL REFERENCES router.credential_subjects(id),
    installation_id uuid NOT NULL REFERENCES router.model_router_installations(id) ON DELETE CASCADE,
    access_enabled boolean NOT NULL DEFAULT false,
    PRIMARY KEY (subject_id, installation_id)
);

ALTER TABLE router.model_router_api_keys ADD COLUMN credential_subject_id uuid;
ALTER TABLE router.model_router_api_keys ADD CONSTRAINT model_router_api_keys_subject_installation_fk
    FOREIGN KEY (credential_subject_id, installation_id)
    REFERENCES router.credential_subject_installations(subject_id, installation_id);
ALTER TABLE router.model_router_api_keys ADD CONSTRAINT model_router_api_keys_personal_routing_only
    CHECK (credential_subject_id IS NULL OR scope = 'routing');

CREATE INDEX model_router_api_keys_subject_idx
    ON router.model_router_api_keys(credential_subject_id) WHERE credential_subject_id IS NOT NULL;

CREATE TABLE router.installation_profile_assignments (
    installation_id uuid PRIMARY KEY REFERENCES router.model_router_installations(id) ON DELETE CASCADE,
    profile_key uuid,
    assignment_generation bigint NOT NULL DEFAULT 0 CHECK (assignment_generation >= 0),
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE router.session_release_bindings (
    installation_id uuid NOT NULL REFERENCES router.model_router_installations(id) ON DELETE CASCADE,
    credential_scope varchar(160) NOT NULL,
    conversation_digest bytea NOT NULL CHECK (octet_length(conversation_digest) = 16),
    target varchar(32) NOT NULL CHECK (target IN ('staging', 'prod/stable', 'prod/weave-internal')),
    activation_id uuid NOT NULL,
    release_sha256 varchar(64) NOT NULL CHECK (release_sha256 ~ '^[0-9a-f]{64}$'),
    binding_sha256 varchar(64) NOT NULL CHECK (binding_sha256 ~ '^[0-9a-f]{64}$'),
    profile_key uuid,
    profile_revision_sha256 varchar(64),
    enrollment_generation bigint NOT NULL CHECK (enrollment_generation >= 0),
    assignment_generation bigint NOT NULL CHECK (assignment_generation >= 0),
    binding_generation bigint NOT NULL CHECK (binding_generation > 0),
    binding jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    last_admitted_at timestamptz NOT NULL,
    PRIMARY KEY (installation_id, credential_scope, conversation_digest),
    CHECK (last_admitted_at >= created_at),
    CHECK ((profile_key IS NULL) = (profile_revision_sha256 IS NULL)),
    CHECK (profile_revision_sha256 IS NULL OR profile_revision_sha256 ~ '^[0-9a-f]{64}$')
);

CREATE INDEX session_release_bindings_activation_idx
    ON router.session_release_bindings(target, activation_id, last_admitted_at);

COMMENT ON TABLE router.credential_subjects IS 'Opaque account-owned identity projection; no private account table dependency';
COMMENT ON TABLE router.installation_profile_assignments IS 'Assignment keys only; exact active revisions are owned by GCS selection sets';
COMMENT ON TABLE router.session_release_bindings IS 'Conversation release pins; admission transactions lock installation, key, subject, then conversation';

CREATE TABLE router.serving_request_attribution (
    request_id TEXT PRIMARY KEY,
    installation_id UUID NOT NULL REFERENCES router.model_router_installations(id) ON DELETE CASCADE,
    api_key_id UUID NOT NULL,
    scope JSONB NOT NULL,
    binding JSONB NOT NULL,
    admitted_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX serving_request_attribution_installation_time
    ON router.serving_request_attribution (installation_id, admitted_at);

COMMIT;
