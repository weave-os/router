BEGIN;

-- Deliberately fails if per-call histories cannot satisfy the legacy contract.
-- Never discard committed predictions to make an old binary serve a new thread.
ALTER TABLE router.classifier_predictions
    ADD CONSTRAINT classifier_predictions_thread_id_user_message_count_key
    UNIQUE (thread_id, user_message_count);
DROP INDEX router.classifier_predictions_call_position;
DROP INDEX router.classifier_predictions_legacy_user_position;
ALTER TABLE router.classifier_predictions DROP COLUMN input_message_count;

ALTER TABLE router.classifier_threads
    DROP CONSTRAINT classifier_thread_prefix_valid,
    DROP COLUMN prefix_digest,
    DROP COLUMN prefix_message_count;

COMMIT;
