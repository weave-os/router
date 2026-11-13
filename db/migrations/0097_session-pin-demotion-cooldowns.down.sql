BEGIN;

ALTER TABLE router.session_pins DROP COLUMN demotion_cooldowns;

COMMIT;
