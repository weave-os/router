BEGIN;

ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN user_prompt BOOLEAN,
    ADD COLUMN user_prompt_gap_ms BIGINT,
    ADD COLUMN user_prompt_gap_prior_model VARCHAR,
    ADD COLUMN error_class VARCHAR,
    ADD COLUMN latest_tool_call_counts JSONB;

CREATE TABLE router.session_turn_clocks (
    installation_id UUID NOT NULL REFERENCES router.model_router_installations (id) ON DELETE CASCADE,
    session_key BYTEA NOT NULL,
    last_response_ended_at TIMESTAMPTZ NOT NULL,
    last_served_model VARCHAR NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (installation_id, session_key)
);

-- The hourly expiry sweep filters on updated_at alone.
CREATE INDEX session_turn_clocks_updated_at_idx ON router.session_turn_clocks (updated_at);

COMMIT;
