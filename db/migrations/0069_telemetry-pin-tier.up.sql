BEGIN;

ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN pin_tier VARCHAR;

COMMENT ON COLUMN router.model_router_request_telemetry.pin_tier IS
    'The actual served-path pin tier for this turn (for example, authoritative_per_turn or hmm_ev_stay_ev_negative). NULL on rows written before this column existed or when no turn-loop tier was available.';

COMMIT;
