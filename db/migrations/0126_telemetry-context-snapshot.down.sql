BEGIN;
ALTER TABLE router.model_router_request_telemetry DROP COLUMN context_snapshot;
COMMIT;
