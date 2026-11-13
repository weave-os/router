BEGIN;

CREATE TABLE router.installation_routing_policies (
    installation_id UUID PRIMARY KEY REFERENCES router.model_router_installations (id) ON DELETE CASCADE,
    mode VARCHAR(16) NOT NULL CHECK (mode IN ('inherit', 'passthrough', 'assigned')),
    revision BIGINT NOT NULL CHECK (revision > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE router.installation_routing_assignments (
    installation_id UUID NOT NULL REFERENCES router.installation_routing_policies (installation_id) ON DELETE CASCADE,
    router_user_id UUID NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    PRIMARY KEY (installation_id, router_user_id),
    FOREIGN KEY (router_user_id, installation_id)
        REFERENCES router.model_router_users (id, installation_id) ON DELETE CASCADE
);

COMMIT;
