-- name: UpdateEscalationCheckFixtureExpired :execrows
-- The opt-in database check expires only its own installation's session.
UPDATE router.escalation_sessions s
SET expires_at = clock_timestamp() - interval '1 second'
FROM router.model_router_installations i
WHERE s.scope = @scope::bytea AND s.installation_id = i.id
    AND i.id = @installation_id::uuid AND i.external_id = @external_id::varchar;

-- name: DeleteEscalationCheckFixture :execrows
-- The opt-in database check removes its installation and cascaded session records.
DELETE FROM router.model_router_installations
WHERE id = @installation_id::uuid AND external_id = @external_id::varchar;

-- name: GetEscalationCheckFixtureRemaining :one
-- The check verifies physical cleanup, including all session-dependent tables.
SELECT (
    (SELECT count(*) FROM router.model_router_installations WHERE id = @installation_id::uuid)
    + (SELECT count(*) FROM router.escalation_sessions WHERE scope = @scope::bytea)
    + (SELECT count(*) FROM router.escalation_checkpoints WHERE scope = @scope::bytea)
    + (SELECT count(*) FROM router.escalation_continuations WHERE scope = @scope::bytea)
    )::bigint AS remaining;
