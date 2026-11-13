BEGIN;

ALTER TABLE router.session_pins
  ADD COLUMN forced_thread_key VARCHAR(32) NOT NULL DEFAULT '';

COMMIT;
