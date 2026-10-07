BEGIN;
ALTER TABLE router.model_router_request_telemetry ADD COLUMN context_snapshot jsonb;
COMMIT;
