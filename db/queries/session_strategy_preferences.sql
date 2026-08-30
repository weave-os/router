-- Reads an explicit beta strategy for one installation-scoped session. A
-- missing or disabled row means the session uses stable routing.
-- name: GetSessionStrategyPreference :one
SELECT strategy
FROM router.session_strategy_preferences
WHERE installation_id = @installation_id::uuid
  AND session_key = @session_key::bytea
  AND enabled;

-- Flips the session's explicit override and returns the state now persisted.
-- The row lock taken on conflict serializes overlapping toggles across router
-- instances, so each caller observes its own flip instead of a stale read.
-- The database constraint rejects any strategy other than hmm_beta.
-- name: UpsertToggledSessionStrategyPreference :one
INSERT INTO router.session_strategy_preferences (
  installation_id, session_key, strategy, enabled
) VALUES (
  @installation_id::uuid, @session_key::bytea, @strategy::varchar, TRUE
)
ON CONFLICT (installation_id, session_key)
DO UPDATE SET
  strategy = EXCLUDED.strategy,
  enabled = NOT router.session_strategy_preferences.enabled
RETURNING enabled;

-- Turns the session's explicit override off and reports one affected row when
-- beta had been enabled. Callers use this instead of the toggle when the beta
-- policy is unavailable, so a concurrent command can never re-enable it.
-- name: UpdateSessionStrategyPreferenceDisabled :execrows
UPDATE router.session_strategy_preferences
SET enabled = FALSE
WHERE installation_id = @installation_id::uuid
  AND session_key = @session_key::bytea
  AND enabled;
