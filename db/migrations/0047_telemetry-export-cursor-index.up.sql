BEGIN;

-- The analytics export pages by an ascending (created_at, id) keyset so an ETL
-- job can resume exactly where it stopped. The existing
-- (installation_id, timestamp DESC) index serves the dashboard's event-time
-- window and cannot produce that order. Every exported row is a
-- router.upstream span, so the partial predicate keeps this index off the
-- other span types entirely.
CREATE INDEX model_router_request_telemetry_export_cursor_idx
    ON router.model_router_request_telemetry (installation_id, created_at, id)
    WHERE span_type = 'router.upstream';

COMMIT;
