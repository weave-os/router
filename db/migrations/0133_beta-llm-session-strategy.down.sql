BEGIN;

DELETE FROM router.session_strategy_preferences WHERE strategy = 'hmm_qwen';

ALTER TABLE router.session_strategy_preferences
    DROP CONSTRAINT session_strategy_preferences_strategy_check,
    ADD CONSTRAINT session_strategy_preferences_strategy_check
        CHECK (strategy = 'hmm_beta');

COMMIT;
