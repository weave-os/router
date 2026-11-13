-- name: UpdateLLMEscalationFixtureLeaseExpired :exec
-- Only the disposable installation's job may be expired by the runtime check.
UPDATE router.llm_escalation_jobs j SET lease_until=clock_timestamp()-interval '1 second'
FROM router.llm_escalation_sessions s
WHERE s.lifetime=j.lifetime AND s.installation_id = @installation_id::uuid AND j.id = @id::uuid;

-- name: UpdateLLMEscalationFixtureSessionExpired :exec
-- Explicit lifetime ownership confines test expiry to the disposable fixture.
UPDATE router.llm_escalation_sessions SET expires_at=clock_timestamp()-interval '1 second'
WHERE installation_id = @installation_id::uuid AND lifetime = @lifetime::uuid;
