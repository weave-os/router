BEGIN;

CREATE TABLE router.session_sibling_preferences (
    installation_id UUID NOT NULL REFERENCES router.model_router_installations(id) ON DELETE CASCADE,
    session_key BYTEA NOT NULL,
    routing_strategy VARCHAR NOT NULL,
    preferences JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (installation_id, session_key, routing_strategy)
);

COMMIT;
