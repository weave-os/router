BEGIN;

-- Nullable as a group: NULL means the snapshot was not recorded, while zero
-- is a measured value. The proxy writes the group only when AI training is
-- allowed and the effective content-capture mode is hashed or full.
ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN spiral_err_streak           INT,
    ADD COLUMN spiral_errored_results      INT,
    ADD COLUMN spiral_tool_results         INT,
    ADD COLUMN spiral_max_same_file_edits  INT,
    ADD COLUMN spiral_same_file_path_hash  VARCHAR,
    ADD COLUMN spiral_repeat_frac          DOUBLE PRECISION,
    ADD COLUMN spiral_monologue_len        INT,
    ADD COLUMN spiral_tool_call_count      INT,
    ADD COLUMN spiral_message_count        INT,
    ADD COLUMN spiral_ping_pong_len        INT,
    ADD COLUMN spiral_steps_since_progress INT,
    ADD COLUMN spiral_edit_attempted       BOOLEAN,
    ADD COLUMN spiral_reasons              VARCHAR[],
    ADD CONSTRAINT model_router_request_telemetry_turn_signals_privacy_check CHECK (
        (
            num_nonnulls(
                spiral_err_streak,
                spiral_errored_results,
                spiral_tool_results,
                spiral_max_same_file_edits,
                spiral_repeat_frac,
                spiral_monologue_len,
                spiral_tool_call_count,
                spiral_message_count,
                spiral_ping_pong_len,
                spiral_steps_since_progress,
                spiral_edit_attempted,
                spiral_reasons
            ) = 0
            AND spiral_same_file_path_hash IS NULL
        )
        OR (
            training_allowed
            AND capture_mode IN ('hashed', 'full')
            AND num_nonnulls(
                spiral_err_streak,
                spiral_errored_results,
                spiral_tool_results,
                spiral_max_same_file_edits,
                spiral_repeat_frac,
                spiral_monologue_len,
                spiral_tool_call_count,
                spiral_message_count,
                spiral_ping_pong_len,
                spiral_steps_since_progress,
                spiral_edit_attempted,
                spiral_reasons
            ) = 12
        )
    );

COMMENT ON COLUMN router.model_router_request_telemetry.spiral_err_streak IS 'Consecutive errored tool_results at the tail of history at request time';
COMMENT ON COLUMN router.model_router_request_telemetry.spiral_errored_results IS 'Errored tool_results across the whole history at request time';
COMMENT ON COLUMN router.model_router_request_telemetry.spiral_tool_results IS 'Total tool_results across the whole history at request time';
COMMENT ON COLUMN router.model_router_request_telemetry.spiral_max_same_file_edits IS 'Edits targeting the single most-edited file path';
COMMENT ON COLUMN router.model_router_request_telemetry.spiral_same_file_path_hash IS 'Truncated sha256 of the most-edited file path: confirms "the same file" across turns without persisting customer file names';
COMMENT ON COLUMN router.model_router_request_telemetry.spiral_repeat_frac IS 'Fraction of the last 12 tool-call signatures that are duplicates; 0 before 12 calls exist';
COMMENT ON COLUMN router.model_router_request_telemetry.spiral_monologue_len IS 'Consecutive tool-less assistant messages since the last real user input';
COMMENT ON COLUMN router.model_router_request_telemetry.spiral_tool_call_count IS 'Assistant tool_use blocks across the whole history at request time';
COMMENT ON COLUMN router.model_router_request_telemetry.spiral_message_count IS 'Messages in the inbound history at request time';
COMMENT ON COLUMN router.model_router_request_telemetry.spiral_ping_pong_len IS 'Length of the trailing A/B/A/B alternation between exactly two tool-call signatures';
COMMENT ON COLUMN router.model_router_request_telemetry.spiral_steps_since_progress IS 'Tool calls made since the last non-errored edit/write result; meaningless unless spiral_edit_attempted is true';
COMMENT ON COLUMN router.model_router_request_telemetry.spiral_edit_attempted IS 'Whether the session has attempted any edit yet; qualifies spiral_steps_since_progress';
COMMENT ON COLUMN router.model_router_request_telemetry.spiral_reasons IS 'Signal classes whose thresholds this turn crossed (err_streak, same_file_thrash, repetition, monologue, ping_pong, no_progress); empty array when the snapshot was recorded and nothing fired';

-- Order recorded turns within a session without indexing excluded rows.
CREATE INDEX model_router_request_telemetry_turn_signals_idx
    ON router.model_router_request_telemetry (session_key, role, timestamp)
    WHERE spiral_tool_call_count IS NOT NULL;

COMMIT;
