BEGIN;

-- Per-USER per-cluster model selection. Sibling to router.cluster_model_lists
-- (which is API-key scoped and remains the org-default layer); this table
-- overrides it per cluster for one router user. Writes are control-plane-owned,
-- like every other settings table in this schema; the router only reads.
--
-- NOTE: models has a cardinality > 0 CHECK, so "the user cleared this cluster"
-- is a DELETE of the row, never an UPDATE to '{}'.
CREATE TABLE router.model_router_user_cluster_model_lists (
    router_user_id  UUID NOT NULL
        REFERENCES router.model_router_users (id) ON DELETE CASCADE,
    cluster_label   VARCHAR(128) NOT NULL,
    organization_id VARCHAR(36) NOT NULL,
    models          TEXT[] NOT NULL,
    created_by      VARCHAR(36),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (router_user_id, cluster_label),
    CONSTRAINT model_router_user_cluster_model_lists_models_not_empty
        CHECK (cardinality(models) > 0)
);

CREATE INDEX model_router_user_cluster_model_lists_organization_id_idx
  ON router.model_router_user_cluster_model_lists (organization_id);

COMMENT ON COLUMN router.model_router_user_cluster_model_lists.cluster_label IS 'Free-form label from the deployed HMM roster artifact (see GET /v1/router/hmm-roster). Not an enum — a roster bump can add or rename clusters.';
COMMENT ON COLUMN router.model_router_user_cluster_model_lists.organization_id IS 'Denormalized for control-plane queries. The router never reads it — lookups are by router_user_id only.';

COMMIT;
