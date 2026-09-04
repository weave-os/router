BEGIN;

DROP VIEW router.production_request_telemetry;

ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN upstream_finish_reason,
    DROP COLUMN stop_reason,
    DROP COLUMN tool_use_blocks,
    DROP COLUMN invalid_tool_args_blocks,
    DROP COLUMN failover_used,
    DROP COLUMN degenerate_shadow;

COMMIT;
