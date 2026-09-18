BEGIN;

ALTER TABLE router.session_pins DROP COLUMN demoted_models;

COMMIT;
