BEGIN;

CREATE INDEX model_router_request_telemetry_session_cost_time_idx
    ON router.model_router_request_telemetry (installation_id, session_id, "timestamp")
    WHERE span_type IN ('router.upstream', 'router.auxiliary_inference')
      AND session_id IS NOT NULL;

DROP INDEX router.model_router_request_telemetry_session_cost_idx;

COMMIT;
