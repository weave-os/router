-- An older observation never overwrites a newer one.
-- name: UpsertRouterUserSeatUsage :exec
INSERT INTO router.model_router_user_seat_usage (
  router_user_id, provider,
  primary_used_percent, primary_window_minutes, primary_resets_at,
  secondary_used_percent, secondary_window_minutes, secondary_resets_at,
  overage_in_use, observed_at
) VALUES (
  @router_user_id::uuid, @provider::varchar,
  @primary_used_percent::double precision, @primary_window_minutes::integer, sqlc.narg('primary_resets_at')::timestamptz,
  @secondary_used_percent::double precision, @secondary_window_minutes::integer, sqlc.narg('secondary_resets_at')::timestamptz,
  @overage_in_use::boolean, @observed_at::timestamptz
)
ON CONFLICT (router_user_id, provider) DO UPDATE SET
  primary_used_percent = EXCLUDED.primary_used_percent,
  primary_window_minutes = EXCLUDED.primary_window_minutes,
  primary_resets_at = EXCLUDED.primary_resets_at,
  secondary_used_percent = EXCLUDED.secondary_used_percent,
  secondary_window_minutes = EXCLUDED.secondary_window_minutes,
  secondary_resets_at = EXCLUDED.secondary_resets_at,
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
