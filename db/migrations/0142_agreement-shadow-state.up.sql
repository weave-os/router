BEGIN;

CREATE TABLE router.agreement_shadow_states (
    installation_id UUID NOT NULL,
    session_key BYTEA NOT NULL,
    role VARCHAR NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    state JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (installation_id, session_key, role)
);

CREATE INDEX agreement_shadow_states_updated_at_idx ON router.agreement_shadow_states (updated_at);

COMMIT;
