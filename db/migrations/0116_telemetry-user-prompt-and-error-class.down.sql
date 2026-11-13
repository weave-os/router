BEGIN;

DROP TABLE router.session_turn_clocks;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN latest_tool_call_counts,
    DROP COLUMN error_class,
    DROP COLUMN user_prompt_gap_prior_model,
    DROP COLUMN user_prompt_gap_ms,
    DROP COLUMN user_prompt;

COMMIT;
