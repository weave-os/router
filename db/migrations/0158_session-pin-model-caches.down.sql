BEGIN;

ALTER TABLE router.session_pins DROP COLUMN model_caches;

COMMIT;
