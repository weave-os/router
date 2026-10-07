BEGIN;

-- failover_used only records a rescue that served the turn; this records that
-- one was dispatched at all, so a failed failover is visible.
ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN failover_attempted BOOLEAN;

COMMIT;
