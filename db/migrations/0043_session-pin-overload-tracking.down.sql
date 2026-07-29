BEGIN;

ALTER TABLE router.session_pins DROP COLUMN disabled_providers;
ALTER TABLE router.session_pins DROP COLUMN consecutive_overload_errors;

COMMIT;
