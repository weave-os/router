BEGIN;

DROP INDEX router.model_router_request_telemetry_session_lookup_idx;
DROP INDEX router.model_router_request_telemetry_session_browse_idx;

COMMIT;
