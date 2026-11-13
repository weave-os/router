BEGIN;

ALTER TABLE router.session_pins DROP COLUMN consecutive_upgrade_votes;

COMMIT;
