BEGIN;

DROP INDEX router.model_router_request_telemetry_turn_signals_idx;

ALTER TABLE router.model_router_request_telemetry
    DROP CONSTRAINT model_router_request_telemetry_turn_signals_privacy_check,
    DROP COLUMN spiral_reasons,
    DROP COLUMN spiral_edit_attempted,
    DROP COLUMN spiral_steps_since_progress,
    DROP COLUMN spiral_ping_pong_len,
    DROP COLUMN spiral_message_count,
    DROP COLUMN spiral_tool_call_count,
    DROP COLUMN spiral_monologue_len,
    DROP COLUMN spiral_repeat_frac,
    DROP COLUMN spiral_same_file_path_hash,
    DROP COLUMN spiral_max_same_file_edits,
    DROP COLUMN spiral_tool_results,
    DROP COLUMN spiral_errored_results,
    DROP COLUMN spiral_err_streak;

COMMIT;
