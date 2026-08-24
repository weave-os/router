-- Reads an explicit beta strategy for one installation-scoped session. A
-- missing row means the session uses stable routing.
-- name: GetSessionStrategyPreference :one
SELECT strategy
FROM router.session_strategy_preferences
WHERE installation_id = @installation_id::uuid
  AND session_key = @session_key::bytea;

-- Records the only explicit per-session strategy override. The database
-- constraint rejects any strategy other than hmm_beta.
-- name: UpsertSessionStrategyPreference :exec
INSERT INTO router.session_strategy_preferences (
  installation_id, session_key, strategy
) VALUES (
  @installation_id::uuid, @session_key::bytea, @strategy::varchar
)
ON CONFLICT (installation_id, session_key)
DO UPDATE SET strategy = EXCLUDED.strategy;

-- Clears the explicit override so the session resumes stable routing.
-- name: DeleteSessionStrategyPreference :exec
DELETE FROM router.session_strategy_preferences
WHERE installation_id = @installation_id::uuid
  AND session_key = @session_key::bytea;
