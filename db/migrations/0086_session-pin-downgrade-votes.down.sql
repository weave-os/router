BEGIN;

ALTER TABLE router.session_pins DROP COLUMN consecutive_downgrade_votes;

COMMIT;
