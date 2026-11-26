BEGIN;

-- Serving profile for an automatic routing model other than weave-auto.
-- weave-auto keeps using installation_profile_assignments; a
-- weave-auto-efficient request reads its row here and is refused without one.
-- routing_model is validated against the Go routing-model registry
-- (router.UsesAssignedProfile), so a new routing model needs no migration.
CREATE TABLE router.installation_routing_model_profile_assignments (
    installation_id uuid NOT NULL REFERENCES router.model_router_installations(id) ON DELETE CASCADE,
    routing_model varchar(64) NOT NULL,
    profile_key uuid NOT NULL,
    assignment_generation bigint NOT NULL DEFAULT 1 CHECK (assignment_generation >= 1),
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (installation_id, routing_model)
);

COMMENT ON TABLE router.installation_routing_model_profile_assignments IS 'Serving profile key per installation for automatic routing models other than weave-auto; exact active revisions are owned by GCS selection sets';

COMMIT;
