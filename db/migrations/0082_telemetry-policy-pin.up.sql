BEGIN;

ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN policy_pin_requested BOOLEAN,
    ADD COLUMN policy_pin_honoured BOOLEAN;

COMMIT;
