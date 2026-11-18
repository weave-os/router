BEGIN;

-- Per-turn session-cost reads use one (installation_id, session_id) equality
-- plus a timestamp range per turn. With session_id as the last key column,
-- every range fetched each of the session's rows from the heap before the
-- time filter applied, so reading a few recent turns still read the whole
-- session. Those rows are scattered across the heap, so a cold cache costs
-- one random page read per row.
--
-- Appending "timestamp" keeps the equality lookup the old index served and
-- lets each turn's range seek directly to its rows.
CREATE INDEX model_router_request_telemetry_session_cost_time_idx
    ON router.model_router_request_telemetry (installation_id, session_id, "timestamp")
    WHERE span_type IN ('router.upstream', 'router.auxiliary_inference')
      AND session_id IS NOT NULL;

DROP INDEX router.model_router_request_telemetry_session_cost_idx;

COMMIT;
