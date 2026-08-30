BEGIN;

ALTER TABLE router.session_pins
    ADD COLUMN routing_strategy VARCHAR(32) NOT NULL DEFAULT '';

CREATE TABLE router.session_strategy_preferences (
    installation_id UUID NOT NULL,
    session_key BYTEA NOT NULL CHECK (octet_length(session_key) = 16),
    strategy VARCHAR(32) NOT NULL CHECK (strategy = 'hmm_beta'),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    PRIMARY KEY (installation_id, session_key),
    FOREIGN KEY (installation_id)
        REFERENCES router.model_router_installations(id) ON DELETE CASCADE
);

COMMENT ON TABLE router.session_strategy_preferences IS
    'Explicit per-session router strategy preferences';

COMMIT;
