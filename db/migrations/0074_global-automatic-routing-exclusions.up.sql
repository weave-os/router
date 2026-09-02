BEGIN;

-- Deployment-wide soft exclusions from AUTOMATIC routing, written by the Weave
-- control plane. Distinct from model_router_installations.excluded_models in
-- both scope and strength: this applies to every installation and only removes
-- a model from automatic selection — an explicit /force-model pin still serves
-- it. Kept as its own table rather than a column so a new installation inherits
-- the setting without a backfill.
CREATE TABLE router.global_automatic_routing_exclusions (
    model TEXT PRIMARY KEY,
    reason TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    created_by TEXT
);

COMMENT ON TABLE router.global_automatic_routing_exclusions IS
  'Deployment-wide models removed from automatic routing (scorer/policy selection, utility hard pins, and automatic session stickies). Soft: an explicit user /force-model pin may still serve these models. Read on the request path through a short-TTL in-process cache.';
COMMENT ON COLUMN router.global_automatic_routing_exclusions.model IS
  'Catalog model ID, validated against the deployed catalog by the writer.';
COMMENT ON COLUMN router.global_automatic_routing_exclusions.reason IS
  'Operator note shown in the internal admin UI and in force-model diagnostics; NULL when none was given.';
COMMENT ON COLUMN router.global_automatic_routing_exclusions.created_by IS
  'Opaque external identifier of the Weave account that disabled the model.';

COMMIT;
