-- Records one shadow-mode struggle detection. Written at detection time with
-- context.Background() (the request ctx may already be canceled); fire rate,
-- precision, and lead time are computed offline by joining session_key
-- against model_router_request_telemetry / session outcomes.
-- name: InsertStruggleShadowEvent :exec
INSERT INTO router.struggle_shadow_events (
    installation_id,
    session_key,
    role,
    routed_model,
    turn_type,
    reason,
    turn_count,
    wall_seconds,
    session_ever_switched,
    est_input_tokens
) VALUES (
    @installation_id::uuid,
    @session_key::bytea,
    @role::varchar,
    @routed_model::varchar,
    @turn_type::varchar,
    @reason::varchar,
    @turn_count::int,
    @wall_seconds::bigint,
    @session_ever_switched::boolean,
    @est_input_tokens::int
);

-- Once-per-(session, role, reason) budget check: any prior event means this
-- operating point already fired for this session and must not produce another
-- row, even across replicas and in-proc cache expiry.
-- name: CountStruggleShadowEvents :one
SELECT COUNT(*)
FROM router.struggle_shadow_events
WHERE session_key = @session_key::bytea
  AND role        = @role::varchar
  AND reason      = @reason::varchar;
