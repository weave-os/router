BEGIN;

CREATE TABLE router.escalation_sessions (
    scope bytea PRIMARY KEY CHECK (octet_length(scope) = 32),
    installation_id uuid NOT NULL REFERENCES router.model_router_installations(id) ON DELETE CASCADE,
    ordinal bigint NOT NULL DEFAULT 0 CHECK (ordinal >= 0),
    session_state jsonb NOT NULL DEFAULT '{}',
    lease_token uuid,
    lease_until timestamptz,
    lease_boundary bytea CHECK (octet_length(lease_boundary) = 32),
    continuity_broken boolean NOT NULL DEFAULT false,
    expires_at timestamptz NOT NULL,
    CHECK ((lease_token IS NULL) = (lease_until IS NULL)),
    CHECK ((lease_token IS NULL) = (lease_boundary IS NULL))
);

CREATE INDEX escalation_sessions_expiry_idx ON router.escalation_sessions (expires_at);
CREATE INDEX escalation_sessions_installation_idx ON router.escalation_sessions (installation_id);

CREATE TABLE router.escalation_checkpoints (
    scope bytea NOT NULL REFERENCES router.escalation_sessions(scope) ON DELETE CASCADE,
    boundary bytea NOT NULL CHECK (octet_length(boundary) = 32),
    checkpoint jsonb NOT NULL,
    PRIMARY KEY (scope, boundary)
);

CREATE TABLE router.escalation_continuations (
    activation bytea NOT NULL CHECK (octet_length(activation) = 32),
    response_digest bytea NOT NULL CHECK (octet_length(response_digest) = 32),
    scope bytea NOT NULL REFERENCES router.escalation_sessions(scope) ON DELETE CASCADE,
    history jsonb NOT NULL CHECK (jsonb_typeof(history) = 'array'),
    PRIMARY KEY (activation, response_digest)
);

CREATE INDEX escalation_continuations_scope_idx ON router.escalation_continuations (scope);

COMMIT;
