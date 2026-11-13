BEGIN;

ALTER TABLE router.struggle_escalation_events
    DROP COLUMN evidence_reasons,
    DROP COLUMN arming_mode;

ALTER TABLE router.spiral_shadow_events
    DROP COLUMN steps_since_progress,
    DROP COLUMN ping_pong_len;

COMMIT;
