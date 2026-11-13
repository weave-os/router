BEGIN;

DROP TABLE router.session_strategy_preferences;

ALTER TABLE router.session_pins
    DROP COLUMN routing_strategy;

COMMIT;
