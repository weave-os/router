BEGIN;

ALTER TABLE router.session_pins
    DROP COLUMN policy_group;

COMMIT;
