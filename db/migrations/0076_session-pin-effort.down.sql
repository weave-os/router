BEGIN;

ALTER TABLE router.session_pins
  DROP COLUMN pinned_effort;

COMMIT;
