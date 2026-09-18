BEGIN;

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
