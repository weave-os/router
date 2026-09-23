BEGIN;

ALTER TABLE router.classifier_threads
    ADD COLUMN prefix_message_count INTEGER NOT NULL DEFAULT 0 CHECK (prefix_message_count >= 0),
    ADD COLUMN prefix_digest TEXT NOT NULL DEFAULT '',
    ADD CONSTRAINT classifier_thread_prefix_valid CHECK (
        (prefix_message_count = 0 AND prefix_digest = '') OR
        (prefix_message_count > 0 AND prefix_digest ~ '^[a-f0-9]{64}$')
    );

-- Zero denotes a legacy human-turn prediction, never a per-call checkpoint.
ALTER TABLE router.classifier_predictions
    ADD COLUMN input_message_count INTEGER NOT NULL DEFAULT 0 CHECK (input_message_count >= 0),
    DROP CONSTRAINT classifier_predictions_thread_id_user_message_count_key;

CREATE UNIQUE INDEX classifier_predictions_call_position
    ON router.classifier_predictions (thread_id, input_message_count)
    WHERE input_message_count > 0;
CREATE UNIQUE INDEX classifier_predictions_legacy_user_position
    ON router.classifier_predictions (thread_id, user_message_count)
    WHERE input_message_count = 0;

COMMIT;
