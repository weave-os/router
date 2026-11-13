BEGIN;

ALTER TABLE router.session_pins DROP COLUMN last_output_limit_at;

COMMIT;
