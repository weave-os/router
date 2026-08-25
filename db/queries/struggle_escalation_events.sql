-- name: InsertStruggleEscalationEvent :exec
INSERT INTO router.struggle_escalation_events (
    installation_id, session_key, role, struggling_model,
    action, escalation_target, turn_count, wall_seconds,
    session_ever_switched, arming_mode, evidence_reasons
) VALUES (
    @installation_id::uuid, @session_key::bytea, @role::varchar,
    @struggling_model::varchar, @action::varchar, @escalation_target::varchar,
    @turn_count::int, @wall_seconds::bigint, @session_ever_switched::boolean,
    @arming_mode::varchar, @evidence_reasons::varchar[]
);

-- name: CountStruggleEscalationEvents :one
SELECT COUNT(*)
FROM router.struggle_escalation_events
WHERE session_key = @session_key::bytea
  AND role = @role::varchar;
