BEGIN;

-- Removing scope must not turn parent-only choices into conversation-wide pins.
UPDATE router.session_pins
SET pinned_provider = '', pinned_model = '', pinned_effort = '',
    decision_reason = 'user_unforced'
WHERE forced_thread_key <> '';

ALTER TABLE router.session_pins DROP COLUMN forced_thread_key;

COMMIT;
