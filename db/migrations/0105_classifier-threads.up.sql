BEGIN;

CREATE TABLE router.classifier_threads (
    thread_id UUID PRIMARY KEY,
    installation_id UUID NOT NULL REFERENCES router.model_router_installations(id) ON DELETE CASCADE,
    credential_sha256 BYTEA NOT NULL CHECK (octet_length(credential_sha256) = 32),
    request_id UUID NOT NULL,
    release TEXT NOT NULL CHECK (release <> ''),
    release_sha256 TEXT NOT NULL CHECK (release_sha256 ~ '^[a-f0-9]{64}$'),
    selection_policy_sha256 TEXT NOT NULL CHECK (selection_policy_sha256 ~ '^[a-f0-9]{64}$'),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (installation_id, credential_sha256, request_id)
);

CREATE TABLE router.classifier_predictions (
    thread_id UUID NOT NULL REFERENCES router.classifier_threads(thread_id) ON DELETE CASCADE,
    turn_digest TEXT NOT NULL CHECK (turn_digest ~ '^[a-f0-9]{64}$'),
    root_turn_digest TEXT NOT NULL CHECK (root_turn_digest ~ '^[a-f0-9]{64}$'),
    user_message_count INTEGER NOT NULL CHECK (user_message_count > 0),
    tool_call_count INTEGER NOT NULL CHECK (tool_call_count >= 0),
    tool_error_count INTEGER NOT NULL CHECK (tool_error_count BETWEEN 0 AND tool_call_count),
    completed_response_count INTEGER NOT NULL CHECK (completed_response_count >= 0),
    complexity SMALLINT NOT NULL CHECK (complexity BETWEEN 0 AND 3),
    probabilities DOUBLE PRECISION[] NOT NULL CHECK (cardinality(probabilities) = 4),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (thread_id, turn_digest),
    UNIQUE (thread_id, user_message_count)
);

COMMIT;
