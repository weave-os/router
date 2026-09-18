BEGIN;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN last_tool_use_name,
    DROP COLUMN last_tool_use_input_bytes,
    DROP COLUMN tool_error_counts;

COMMIT;
