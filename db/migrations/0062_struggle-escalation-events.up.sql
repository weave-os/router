BEGIN;

-- struggle_escalation_events records every armed struggle detection.
-- One row per (session_key, role). The action column records what happened
-- ('sideways', 'holdout', 'disabled', 'user_forced', 'no_sideways_target',
-- 'no_eligible_arms'). Joined offline against router_calls + session outcomes
-- to measure win-rate vs holdout.
CREATE TABLE router.struggle_escalation_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    installation_id UUID NOT NULL REFERENCES router.model_router_installations(id) ON DELETE CASCADE,
    session_key BYTEA NOT NULL,
    role VARCHAR NOT NULL,
    struggling_model VARCHAR NOT NULL,
    action VARCHAR NOT NULL,
    escalation_target VARCHAR NOT NULL DEFAULT '',
    turn_count INT NOT NULL,
    wall_seconds BIGINT NOT NULL,
    session_ever_switched BOOLEAN NOT NULL
);

CREATE UNIQUE INDEX struggle_escalation_events_session_role_idx
    ON router.struggle_escalation_events (session_key, role);
CREATE INDEX struggle_escalation_events_installation_created_idx
    ON router.struggle_escalation_events (installation_id, created_at DESC);

COMMIT;
