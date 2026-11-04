BEGIN;

-- Verbatim x-codex-{primary,secondary,credits}-* header set per
-- Codex-subscription-served turn, so per-seat quota is visible after the
-- in-memory usage observer forgets it. NULL on other turns.
ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN codex_limit_headers JSONB;

COMMIT;
