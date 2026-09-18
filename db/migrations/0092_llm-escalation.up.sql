BEGIN;
CREATE TABLE router.llm_escalation_sessions (
    scope bytea PRIMARY KEY CHECK (octet_length(scope) = 32),
    lifetime uuid NOT NULL UNIQUE,
    installation_id uuid NOT NULL REFERENCES router.model_router_installations(id) ON DELETE CASCADE,
    state jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX llm_escalation_sessions_expiry ON router.llm_escalation_sessions(expires_at);
CREATE INDEX llm_escalation_sessions_installation ON router.llm_escalation_sessions(installation_id, updated_at DESC);
CREATE TABLE router.llm_escalation_completions (
    lifetime uuid NOT NULL REFERENCES router.llm_escalation_sessions(lifetime) ON DELETE CASCADE,
    boundary bytea NOT NULL CHECK (octet_length(boundary) = 32),
    PRIMARY KEY(lifetime, boundary)
);
CREATE TABLE router.llm_escalation_jobs (
    id uuid PRIMARY KEY,
    lifetime uuid NOT NULL REFERENCES router.llm_escalation_sessions(lifetime) ON DELETE CASCADE,
    generation bigint NOT NULL,
    checkpoint bigint NOT NULL,
    status text NOT NULL,
    lease_until timestamptz NOT NULL,
    job jsonb NOT NULL,
    UNIQUE(lifetime, checkpoint)
);
CREATE INDEX llm_escalation_jobs_lifetime ON router.llm_escalation_jobs(lifetime);
CREATE INDEX llm_escalation_jobs_lease ON router.llm_escalation_jobs(status, lease_until);
CREATE TABLE router.llm_escalation_continuations (
    activation bytea NOT NULL CHECK (octet_length(activation) = 32),
    response_digest bytea NOT NULL CHECK (octet_length(response_digest) = 32),
    lifetime uuid NOT NULL REFERENCES router.llm_escalation_sessions(lifetime) ON DELETE CASCADE,
    history jsonb NOT NULL CHECK(jsonb_typeof(history) = 'array'),
    PRIMARY KEY(activation, response_digest)
);
CREATE INDEX llm_escalation_continuations_lifetime ON router.llm_escalation_continuations(lifetime);
COMMIT;
