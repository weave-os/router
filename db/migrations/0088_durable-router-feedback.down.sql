BEGIN;

DROP INDEX router.router_feedback_pending_idx;
ALTER TABLE router.router_feedback
    DROP CONSTRAINT router_feedback_lease_pair,
    DROP COLUMN last_error,
    DROP COLUMN lease_until,
    DROP COLUMN lease_token,
    DROP COLUMN next_attempt_at,
    DROP COLUMN attempts,
    DROP COLUMN delivery_status,
    DROP COLUMN training_allowed,
    DROP COLUMN rollout_id,
    DROP COLUMN served_provider,
    DROP COLUMN strategy,
    DROP COLUMN target_sequence,
    DROP COLUMN requested_sequence,
    DROP COLUMN external_id;
DROP TABLE router.feedback_request_history;
DROP TABLE router.feedback_history_scopes;

COMMIT;
