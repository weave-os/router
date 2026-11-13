BEGIN;

ALTER TABLE router.session_pins DROP COLUMN tokenizer_calibration;

COMMIT;
