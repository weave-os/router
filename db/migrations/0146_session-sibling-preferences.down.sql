BEGIN;

DROP TABLE router.session_sibling_preferences;

ALTER TABLE router.session_pins DROP COLUMN model_failure_at;

COMMIT;
