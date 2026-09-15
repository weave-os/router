-- name: DeleteExpiredLLMEscalationScope :exec
-- A recreated scope gets a new lifetime, fencing old workers.
DELETE FROM router.llm_escalation_sessions WHERE scope = @scope::bytea AND expires_at <= clock_timestamp();

-- name: InsertLLMEscalationSession :exec
-- Concurrent starts share the existing lifetime.
INSERT INTO router.llm_escalation_sessions(scope,lifetime,installation_id,state,expires_at)
VALUES(@scope::bytea, @lifetime::uuid, @installation_id::uuid, @state::jsonb,clock_timestamp()+interval '24 hours')
ON CONFLICT(scope) DO NOTHING;

-- name: GetLLMEscalationSessionLocked :one
-- Session bookkeeping is serialized only within short transactions.
SELECT state FROM router.llm_escalation_sessions
WHERE scope= @scope::bytea AND expires_at>clock_timestamp() FOR UPDATE;

-- name: UpdateLLMEscalationSession :execrows
-- Lifetime ownership prevents a stale request from resurrecting expired state.
UPDATE router.llm_escalation_sessions SET state= @state::jsonb,updated_at=clock_timestamp(),expires_at=clock_timestamp()+interval '24 hours'
WHERE scope= @scope::bytea AND lifetime= @lifetime::uuid AND expires_at>clock_timestamp();

-- name: InsertLLMEscalationCompletion :execrows
-- Duplicate responses neither advance cadence nor create another paid checkpoint.
INSERT INTO router.llm_escalation_completions(lifetime,boundary)
VALUES(@lifetime::uuid, @boundary::bytea) ON CONFLICT DO NOTHING;

-- name: InsertLLMEscalationJob :exec
-- A checkpoint retains bounded operational metadata, never the transcript.
INSERT INTO router.llm_escalation_jobs(id,lifetime,generation,checkpoint,status,lease_until,job)
VALUES(@id::uuid, @lifetime::uuid, @generation::bigint, @checkpoint::bigint, @status::text,clock_timestamp()+interval '30 seconds', @job::jsonb);

-- name: GetLLMEscalationLiveJob :one
-- A running paid request remains in flight until its lease expires.
SELECT id FROM router.llm_escalation_jobs WHERE lifetime= @lifetime::uuid
AND status= @status::text AND lease_until>clock_timestamp() LIMIT 1;

-- name: GetLLMEscalationCheckpointJob :one
-- Fetch the current generation's verdict while its session is locked.
SELECT job FROM router.llm_escalation_jobs
WHERE lifetime= @lifetime::uuid AND generation= @generation::bigint AND checkpoint= @checkpoint::bigint;

-- name: UpdateLLMEscalationJobFinished :execrows
-- A result cannot outlive its session, generation, checkpoint, or inference lease.
UPDATE router.llm_escalation_jobs j SET status= @status::text,job= @job::jsonb
FROM router.llm_escalation_sessions s
WHERE j.id= @id::uuid AND j.lifetime= @lifetime::uuid AND j.status= @running_status::text
AND j.lease_until>clock_timestamp() AND s.lifetime=j.lifetime AND s.expires_at>clock_timestamp()
AND (s.state->>'generation')::bigint=j.generation AND (s.state->>'latest_checkpoint')::bigint=j.checkpoint;

-- name: UpdateLLMEscalationJobApplied :execrows
-- Applied metadata is written under the owning session's short lock.
UPDATE router.llm_escalation_jobs SET status= @status::text,job= @job::jsonb
WHERE id= @id::uuid AND lifetime= @lifetime::uuid AND status= @completed_status::text;

-- name: UpdateLLMEscalationJobMetadata :execrows
-- Records an application attempt without consuming the pending verdict.
UPDATE router.llm_escalation_jobs SET job= @job::jsonb
WHERE id= @id::uuid AND lifetime= @lifetime::uuid AND status= @completed_status::text;

-- name: InsertLLMEscalationContinuation :exec
-- Immutable response histories remain available after later turns advance.
INSERT INTO router.llm_escalation_continuations(activation,response_digest,lifetime,history)
SELECT @activation::bytea, @response_digest::bytea,lifetime, @history::jsonb
FROM router.llm_escalation_sessions WHERE lifetime= @lifetime::uuid AND expires_at>clock_timestamp()
ON CONFLICT(activation,response_digest) DO NOTHING;

-- name: GetLLMEscalationContinuation :one
-- Continuations are isolated by activation and expire with the session.
SELECT s.scope,c.history FROM router.llm_escalation_continuations c
JOIN router.llm_escalation_sessions s ON s.lifetime=c.lifetime
WHERE c.activation= @activation::bytea AND c.response_digest= @response_digest::bytea AND s.expires_at>clock_timestamp();

-- name: GetLLMEscalationJobs :many
-- Installation-scoped audit listing contains no captured transcript.
SELECT j.job FROM router.llm_escalation_jobs j JOIN router.llm_escalation_sessions s ON s.lifetime=j.lifetime
WHERE s.installation_id= @installation_id::uuid AND s.expires_at>clock_timestamp()
ORDER BY s.updated_at DESC,j.checkpoint DESC LIMIT @page_limit::int;

-- name: GetLLMEscalationJob :one
-- A job id alone never grants cross-installation access.
SELECT j.job FROM router.llm_escalation_jobs j JOIN router.llm_escalation_sessions s ON s.lifetime=j.lifetime
WHERE s.installation_id= @installation_id::uuid AND j.id= @id::uuid AND s.expires_at>clock_timestamp();

-- name: GetLLMEscalationSessions :many
-- Page sessions using a stable lifetime tie breaker.
SELECT state FROM router.llm_escalation_sessions
WHERE (@all_installations::boolean OR installation_id= @installation_id::uuid)
  AND expires_at>clock_timestamp()
ORDER BY updated_at DESC,lifetime DESC LIMIT @page_limit::int OFFSET @page_offset::int;

-- name: GetLLMEscalationSessionDetail :one
-- Session detail is scoped by installation and its public digest.
SELECT state FROM router.llm_escalation_sessions WHERE installation_id= @installation_id::uuid AND scope= @scope::bytea AND expires_at>clock_timestamp();

-- name: GetLLMEscalationSessionJobs :many
-- Detail checkpoints remain in source-turn order.
SELECT job FROM router.llm_escalation_jobs WHERE lifetime= @lifetime::uuid ORDER BY checkpoint DESC;

-- name: GetLLMEscalationSummary :one
-- Aggregate only bounded operational metadata; rationale and transcript are excluded.
SELECT
  count(*) FILTER (WHERE j.job->'judgment'->>'escalate' = 'true')::bigint AS positive_judgments,
  count(*) FILTER (WHERE j.status = 'applied' AND s.state->'config'->>'mode' = 'active')::bigint AS actual_interventions,
  count(*) FILTER (WHERE j.status = 'applied' AND s.state->'config'->>'mode' = 'shadow')::bigint AS shadow_interventions,
  count(*) FILTER (WHERE j.status = 'stale')::bigint AS stale_results,
  count(*) FILTER (WHERE j.job->>'failure' = 'timeout')::bigint AS timeouts,
  count(*) FILTER (WHERE j.job->>'failure' = 'invalid_response')::bigint AS invalid_responses,
  count(*) FILTER (WHERE j.job->>'failure' = 'capacity')::bigint AS capacity_skips,
  count(*) FILTER (WHERE j.job->>'failure' = 'call_limit')::bigint AS attempt_limit_exhaustion
FROM router.llm_escalation_jobs j
JOIN router.llm_escalation_sessions s ON s.lifetime = j.lifetime
WHERE (@all_installations::boolean OR s.installation_id = @installation_id::uuid)
  AND s.expires_at > clock_timestamp();

-- name: DeleteExpiredLLMEscalationSessions :exec
-- Cascade operational metadata and continuation content together.
DELETE FROM router.llm_escalation_sessions WHERE expires_at<=clock_timestamp();

-- name: ExpireLLMEscalationJobLeases :exec
-- Process loss leaves no durable transcript to retry; close expired claims as missed checks.
UPDATE router.llm_escalation_jobs
SET status = 'failed',
    job = jsonb_set(
      jsonb_set(
        jsonb_set(job, '{status}', '"failed"'::jsonb),
        '{failure}', '"lease_expired"'::jsonb
      ),
      '{finished_at}', to_jsonb(clock_timestamp())
    )
WHERE status = 'running' AND lease_until <= clock_timestamp();

-- name: UpdateLLMEscalationJobStale :exec
-- Stale work retains measured spend but cannot become an applicable verdict.
UPDATE router.llm_escalation_jobs SET status = @status::text,job = @job::jsonb
WHERE id = @id::uuid AND lifetime = @lifetime::uuid AND status = @running_status::text;
