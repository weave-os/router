BEGIN;

CREATE TABLE router.feedback_history_scopes (
    installation_id uuid NOT NULL REFERENCES router.model_router_installations(id) ON DELETE CASCADE,
    session_key bytea NOT NULL CHECK (octet_length(session_key) = 16),
    role varchar NOT NULL,
    last_sequence bigint NOT NULL DEFAULT 0 CHECK (last_sequence >= 0),
    PRIMARY KEY (installation_id, session_key, role)
);

CREATE TABLE router.feedback_request_history (
    installation_id uuid NOT NULL,
    session_key bytea NOT NULL,
    role varchar NOT NULL,
    sequence bigint NOT NULL CHECK (sequence > 0),
    request_id varchar NOT NULL CHECK (request_id <> ''),
    completed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    served_model varchar NOT NULL,
    served_provider varchar NOT NULL,
    strategy varchar NOT NULL DEFAULT '',
    route_id varchar NOT NULL DEFAULT '',
    training_allowed boolean NOT NULL DEFAULT false,
    PRIMARY KEY (installation_id, session_key, role, sequence),
    UNIQUE (installation_id, request_id),
    FOREIGN KEY (installation_id, session_key, role)
        REFERENCES router.feedback_history_scopes (installation_id, session_key, role) ON DELETE CASCADE
);

ALTER TABLE router.router_feedback
    ADD COLUMN external_id varchar NOT NULL DEFAULT '',
    ADD COLUMN requested_sequence integer NOT NULL DEFAULT -1 CHECK (requested_sequence BETWEEN -99 AND 99 AND requested_sequence <> 0),
    ADD COLUMN target_sequence bigint NOT NULL DEFAULT 0 CHECK (target_sequence >= 0),
    ADD COLUMN strategy varchar NOT NULL DEFAULT '',
    ADD COLUMN served_provider varchar NOT NULL DEFAULT '',
    ADD COLUMN rollout_id varchar NOT NULL DEFAULT '',
    ADD COLUMN training_allowed boolean NOT NULL DEFAULT false,
    ADD COLUMN delivery_status varchar NOT NULL DEFAULT 'skipped' CHECK (delivery_status IN ('pending', 'delivered', 'skipped')),
    ADD COLUMN attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN lease_token uuid,
    ADD COLUMN lease_until timestamptz,
    ADD COLUMN last_error text NOT NULL DEFAULT '',
    ADD CONSTRAINT router_feedback_lease_pair CHECK ((lease_token IS NULL) = (lease_until IS NULL));

CREATE INDEX router_feedback_pending_idx ON router.router_feedback (next_attempt_at, created_at, id)
    WHERE delivery_status = 'pending';

COMMIT;
