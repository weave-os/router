BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN policy_pin_honoured,
    DROP COLUMN policy_pin_requested;

COMMIT;
