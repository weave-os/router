BEGIN;

ALTER TABLE router.session_pins
  DROP COLUMN turn_strikes;

COMMIT;
