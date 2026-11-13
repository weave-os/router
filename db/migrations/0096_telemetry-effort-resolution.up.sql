BEGIN;

-- Persist the per-turn effort resolution onto the existing telemetry row.
-- Today these exist only as `routing.*_effort` span attributes, so the level a
-- turn actually dispatched with is unreadable from Postgres after the fact --
-- which leaves reasoning-driven cost gaps (routed Claude Code emitting ~2.5x
-- the hidden thinking of a direct control) impossible to attribute offline.
-- Nullable throughout: resolveEffort returns an empty resolution on targets
-- that express no effort, and an empty string must not read as "resolved".
--
-- effort_mismatch is deliberately absent: it is derivable from arm vs sent.
ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN effort_arm      VARCHAR,
    ADD COLUMN effort_selected VARCHAR,
    ADD COLUMN effort_sent     VARCHAR,
    ADD COLUMN effort_source   VARCHAR;

COMMENT ON COLUMN router.model_router_request_telemetry.effort_arm IS
    'Effort level the policy arm is labelled with. NULL when the arm carries none.';
COMMENT ON COLUMN router.model_router_request_telemetry.effort_selected IS
    'Effort level that won precedence (user > escalation > arm > model policy), pre-clamp.';
COMMENT ON COLUMN router.model_router_request_telemetry.effort_sent IS
    'Effort level written on the wire after the target menu clamp (xhigh -> max -> high). NULL when nothing was sent.';
COMMENT ON COLUMN router.model_router_request_telemetry.effort_source IS
    'Precedence branch that produced the level: user, escalation, arm, model_policy. NULL when no effort resolved.';

COMMIT;
