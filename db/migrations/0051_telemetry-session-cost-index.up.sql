BEGIN;

-- The public Weave session-cost endpoint aggregates one client session's
-- committed upstream telemetry: an (installation_id, session_id) equality
-- lookup scoped through router.model_router_installations.external_id. Neither
-- existing index serves it — (installation_id, timestamp DESC) is event-time
-- ordered and (installation_id, created_at, id) is the export keyset — so the
-- lookup would degrade to an installation-wide scan at production volume.
--
-- The partial predicate keeps the index off non-cost span types and off the
-- ~6% of rows whose client sent no session header; both are excluded by the
-- endpoint's query, so the index stays proportional to session-tagged
-- cost-bearing traffic.
CREATE INDEX model_router_request_telemetry_session_cost_idx
    ON router.model_router_request_telemetry (installation_id, session_id)
    WHERE span_type IN ('router.upstream', 'router.auxiliary_inference')
      AND session_id IS NOT NULL;

COMMIT;
