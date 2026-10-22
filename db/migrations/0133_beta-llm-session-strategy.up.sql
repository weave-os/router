BEGIN;

ALTER TABLE router.session_strategy_preferences
    DROP CONSTRAINT session_strategy_preferences_strategy_check,
    ADD CONSTRAINT session_strategy_preferences_strategy_check
        CHECK (strategy IN ('hmm_beta', 'hmm_qwen'));

COMMIT;
