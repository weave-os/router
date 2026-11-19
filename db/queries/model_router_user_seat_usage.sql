-- An older observation never overwrites a newer one. A window the provider
-- omitted keeps its previous value until that window resets.
-- name: UpsertRouterUserSeatUsage :exec
INSERT INTO router.model_router_user_seat_usage (
  router_user_id, provider,
  primary_used_percent, primary_window_minutes, primary_resets_at,
  secondary_used_percent, secondary_window_minutes, secondary_resets_at,
  overage_in_use, observed_at
) VALUES (
  @router_user_id::uuid, @provider::varchar,
  @primary_used_percent::double precision, @primary_window_minutes::bigint, sqlc.narg('primary_resets_at')::timestamptz,
  @secondary_used_percent::double precision, @secondary_window_minutes::bigint, sqlc.narg('secondary_resets_at')::timestamptz,
  @overage_in_use::boolean, @observed_at::timestamptz
)
ON CONFLICT (router_user_id, provider) DO UPDATE SET
  primary_used_percent = CASE WHEN (EXCLUDED.primary_window_minutes = 0 AND router.model_router_user_seat_usage.primary_resets_at > EXCLUDED.observed_at) THEN router.model_router_user_seat_usage.primary_used_percent ELSE EXCLUDED.primary_used_percent END,
  primary_window_minutes = CASE WHEN (EXCLUDED.primary_window_minutes = 0 AND router.model_router_user_seat_usage.primary_resets_at > EXCLUDED.observed_at) THEN router.model_router_user_seat_usage.primary_window_minutes ELSE EXCLUDED.primary_window_minutes END,
  primary_resets_at = CASE WHEN (EXCLUDED.primary_window_minutes = 0 AND router.model_router_user_seat_usage.primary_resets_at > EXCLUDED.observed_at) THEN router.model_router_user_seat_usage.primary_resets_at ELSE EXCLUDED.primary_resets_at END,
  secondary_used_percent = CASE WHEN (EXCLUDED.secondary_window_minutes = 0 AND router.model_router_user_seat_usage.secondary_resets_at > EXCLUDED.observed_at) THEN router.model_router_user_seat_usage.secondary_used_percent ELSE EXCLUDED.secondary_used_percent END,
  secondary_window_minutes = CASE WHEN (EXCLUDED.secondary_window_minutes = 0 AND router.model_router_user_seat_usage.secondary_resets_at > EXCLUDED.observed_at) THEN router.model_router_user_seat_usage.secondary_window_minutes ELSE EXCLUDED.secondary_window_minutes END,
  secondary_resets_at = CASE WHEN (EXCLUDED.secondary_window_minutes = 0 AND router.model_router_user_seat_usage.secondary_resets_at > EXCLUDED.observed_at) THEN router.model_router_user_seat_usage.secondary_resets_at ELSE EXCLUDED.secondary_resets_at END,
  overage_in_use = EXCLUDED.overage_in_use,
  observed_at = EXCLUDED.observed_at
WHERE router.model_router_user_seat_usage.observed_at < EXCLUDED.observed_at;

-- name: ListRouterUserSeatUsage :many
SELECT provider,
       primary_used_percent, primary_window_minutes, primary_resets_at,
       secondary_used_percent, secondary_window_minutes, secondary_resets_at,
       overage_in_use, observed_at
FROM router.model_router_user_seat_usage
WHERE router_user_id = @router_user_id::uuid
ORDER BY provider;
