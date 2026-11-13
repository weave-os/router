BEGIN;

-- Observation-only detail for tool-driven turns. A turn that ends on
-- stop_reason = 'tool_use' hands the client one last tool call to run; when
-- the session then dies, tool_use_blocks alone cannot say which tool or how
-- large the call was. tool_error_counts records, per tool name, how many of
-- this request's tool calls received a tool_result and how many of those
-- results errored, so per-tool error rates can be read off the row without
-- re-parsing request bodies. Nothing on the request path reads these columns.
ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN last_tool_use_name VARCHAR,
    ADD COLUMN last_tool_use_input_bytes INT,
    ADD COLUMN tool_error_counts JSONB;

COMMENT ON COLUMN router.model_router_request_telemetry.last_tool_use_name IS
    'Name of the final tool_use block on a turn that ended with stop_reason = tool_use. NULL on every other turn.';
COMMENT ON COLUMN router.model_router_request_telemetry.last_tool_use_input_bytes IS
    'Byte length of the final tool_use block''s input JSON as sent to the client, on a turn that ended with stop_reason = tool_use. NULL on every other turn.';
COMMENT ON COLUMN router.model_router_request_telemetry.tool_error_counts IS
    'JSON object of tool name to {calls, errors}: resolved tool calls on this request and how many of their results errored. NULL when the history has no resolved tool call.';

COMMIT;
