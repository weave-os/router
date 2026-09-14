BEGIN;

-- Observation-only: whether the router appended its non-interactive operating
-- instruction to this request's system prompt (flag cc_autonomy_system_append).
-- Lets an eval run be audited for which turns actually carried the append.
-- Nothing on the request path reads this column.
ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN autonomy_append_fired BOOLEAN;

COMMENT ON COLUMN router.model_router_request_telemetry.autonomy_append_fired IS
    'TRUE when the router appended the autonomy operating instruction to the outgoing system prompt on this request. NULL when it did not.';

COMMIT;
