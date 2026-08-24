-- Reads the active pin for a (session_key, role) pair. Single-row;
-- returns sql.ErrNoRows when no pin is recorded yet. The caller checks
-- pinned_until against now() to discard expired rows that the hourly
-- sweep hasn't collected yet. The last_* token columns and
-- last_turn_ended_at carry the previous turn's upstream usage; the
-- planner reads them to weigh switch EV against eviction cost.
-- name: GetSessionPin :one
SELECT *
FROM router.session_pins
WHERE session_key = @session_key::bytea
  AND role        = @role::varchar;

-- Atomically consumes one active pin for the expected strategy so a stale
-- continuation cannot delete a replacement strategy's pin. Expired rows
-- remain for the normal sweep.
-- name: DeleteSessionPin :one
DELETE FROM router.session_pins
WHERE session_key = @session_key::bytea
  AND role        = @role::varchar
  AND (
    routing_strategy = @expected_routing_strategy::varchar
    OR (routing_strategy = '' AND @expected_routing_strategy::varchar <> 'hmm_beta')
  )
  AND pinned_until > CURRENT_TIMESTAMP
RETURNING *;

-- Upserts a pin, refreshing pinned_until on every hit (sliding TTL).
-- turn_count increments on conflict so we can observe how many turns a
-- single (session_key, role) lives for. installation_id is set on first
-- insert and not touched on update — re-binding a session to a different
-- installation would indicate a bug, not a legitimate state. The
-- last_*_tokens / last_turn_ended_at columns are deliberately omitted
-- from the ON CONFLICT update set: only UpdateSessionPinUsage writes
-- them, so the at-start-of-turn refresh here cannot clobber the
-- previous turn's usage with zeros before the planner reads it.
--
-- consecutive_upstream_errors is preserved on a same-model, same-strategy
-- refresh (so the two-strike eviction counter accumulates across turns of the
-- same sticky pin) but reset to 0 on a model or strategy switch. The reset also
-- covers loop-break / force-model pin-expiry writes, which set pinned_model to
-- the empty string.
--
-- paired_provider / paired_model hold the runner-up half of the band pair the
-- scorer picks. On the conflict update they refresh to a fresh scorer runner-up
-- (non-empty incoming pair), are preserved when both the pinned model and
-- strategy are unchanged (sticky refresh / same-model re-anchor carry an empty
-- pair), and are cleared when either changes without a fresh pair. This keeps
-- the stored pair consistent with the live decision: it tracks genuine
-- re-routes and never inherits a stale runner-up across a strategy change.
--
-- policy_group follows the same three-way maintenance: a fresh policy decision
-- supplies a non-empty group, a same-model same-strategy refresh preserves the
-- stored one, and a model or strategy change without a group clears it.
-- The pin-sticky arm-selector guard compares it against the fresh decision's
-- group, so a stale group must never survive onto a different pinned model.
-- name: UpsertSessionPin :exec
INSERT INTO router.session_pins (
  session_key, role, installation_id, pinned_provider,
  pinned_model, paired_provider, paired_model,
  decision_reason, routing_strategy, policy_group, turn_count, pinned_until
) VALUES (
  @session_key::bytea, @role::varchar, @installation_id::uuid,
  @pinned_provider::varchar, @pinned_model::varchar,
  @paired_provider::varchar, @paired_model::varchar,
  @decision_reason::text, @routing_strategy::varchar, @policy_group::varchar,
  @turn_count::int, @pinned_until::timestamp
)
ON CONFLICT (session_key, role) DO UPDATE SET
  pinned_provider = EXCLUDED.pinned_provider,
  pinned_model    = EXCLUDED.pinned_model,
  decision_reason = EXCLUDED.decision_reason,
  routing_strategy = EXCLUDED.routing_strategy,
  turn_count      = CASE
    WHEN router.session_pins.routing_strategy = EXCLUDED.routing_strategy
      THEN router.session_pins.turn_count + 1
    ELSE EXCLUDED.turn_count
  END,
  pinned_until    = EXCLUDED.pinned_until,
  first_pinned_at = CASE
    WHEN router.session_pins.routing_strategy = EXCLUDED.routing_strategy
      THEN router.session_pins.first_pinned_at
    ELSE CURRENT_TIMESTAMP
  END,
  last_seen_at    = CURRENT_TIMESTAMP,
  -- Band pair maintenance, in priority order:
  --   1. A fresh scorer decision supplies a non-empty pair -> take it.
  --   2. Empty incoming pair but model and strategy are unchanged -> preserve it.
  --   3. Empty incoming pair and either changed -> clear it.
  paired_provider = CASE
    WHEN EXCLUDED.paired_model <> ''
      THEN EXCLUDED.paired_provider
    WHEN EXCLUDED.pinned_model = router.session_pins.pinned_model
      AND EXCLUDED.routing_strategy = router.session_pins.routing_strategy
      THEN router.session_pins.paired_provider
    ELSE ''
  END,
  paired_model = CASE
    WHEN EXCLUDED.paired_model <> ''
      THEN EXCLUDED.paired_model
    WHEN EXCLUDED.pinned_model = router.session_pins.pinned_model
      AND EXCLUDED.routing_strategy = router.session_pins.routing_strategy
      THEN router.session_pins.paired_model
    ELSE ''
  END,
  policy_group = CASE
    WHEN EXCLUDED.policy_group <> ''
      THEN EXCLUDED.policy_group
    WHEN EXCLUDED.pinned_model = router.session_pins.pinned_model
      AND EXCLUDED.routing_strategy = router.session_pins.routing_strategy
      THEN router.session_pins.policy_group
    ELSE ''
  END,
  consecutive_upstream_errors = CASE
    WHEN router.session_pins.pinned_model = EXCLUDED.pinned_model
      AND router.session_pins.routing_strategy = EXCLUDED.routing_strategy
      THEN router.session_pins.consecutive_upstream_errors
    ELSE 0
  END,
  -- Mirrors consecutive_upstream_errors above: a stray 529 strike must not
  -- survive an unrelated pin rewrite (degenerate eviction, loop-break, 4xx
  -- eviction, planner switch) onto a different model, or one 529 on the
  -- fresh pin could immediately hit the two-strike threshold instead of
  -- requiring two genuine consecutive strikes on the SAME served provider.
  consecutive_overload_errors = CASE
    WHEN router.session_pins.pinned_model = EXCLUDED.pinned_model
      AND router.session_pins.routing_strategy = EXCLUDED.routing_strategy
      THEN router.session_pins.consecutive_overload_errors
    ELSE 0
  END,
  -- A strategy switch selects a different policy. Do not carry cache,
  -- switch, or error evidence from the previous policy into it.
  last_input_tokens = CASE
    WHEN router.session_pins.routing_strategy = EXCLUDED.routing_strategy
      THEN router.session_pins.last_input_tokens
    ELSE 0
  END,
  last_cached_read_tokens = CASE
    WHEN router.session_pins.routing_strategy = EXCLUDED.routing_strategy
      THEN router.session_pins.last_cached_read_tokens
    ELSE 0
  END,
  last_cached_write_tokens = CASE
    WHEN router.session_pins.routing_strategy = EXCLUDED.routing_strategy
      THEN router.session_pins.last_cached_write_tokens
    ELSE 0
  END,
  last_output_tokens = CASE
    WHEN router.session_pins.routing_strategy = EXCLUDED.routing_strategy
      THEN router.session_pins.last_output_tokens
    ELSE 0
  END,
  last_turn_ended_at = CASE
    WHEN router.session_pins.routing_strategy = EXCLUDED.routing_strategy
      THEN router.session_pins.last_turn_ended_at
    ELSE NULL
  END,
  last_served_model = CASE
    WHEN router.session_pins.routing_strategy = EXCLUDED.routing_strategy
      THEN router.session_pins.last_served_model
    ELSE ''
  END,
  has_ever_switched = CASE
    WHEN router.session_pins.routing_strategy = EXCLUDED.routing_strategy
      THEN router.session_pins.has_ever_switched
    ELSE FALSE
  END,
  disabled_providers = CASE
    WHEN router.session_pins.routing_strategy = EXCLUDED.routing_strategy
      THEN router.session_pins.disabled_providers
    ELSE '{}'
  END;

-- Records the previous turn's upstream token usage on an existing pin
-- row. Fired off the request path after the upstream response
-- completes; the planner reads these columns at the start of the next
-- turn to compute switch EV against eviction cost. The UPDATE matches
-- by (session_key, role); if the pin has been evicted or never
-- existed, zero rows are affected and the adapter wraps that as a
-- successful no-op. A strategy mismatch is also a no-op, preventing a late
-- response from mutating a replacement strategy's pin. last_served_model records the model that actually
-- served this turn; it lives here (not in UpsertSessionPin) so a
-- /force-model upsert cannot overwrite the genuinely-last-served model
-- before the next turn reads it to detect a mid-session model switch.
-- pinned_provider is updated to the binding that actually served so per-turn
-- policies receive correct previous-provider cache affinity after fallback.
-- has_ever_switched latches true the first time the just-served model
-- differs from a prior non-empty last_served_model. Caller-supplied model and
-- latch evidence preserves history when the stored role row is new.
-- The latch keeps stripping stale thinking signatures on later turns because
-- clients resend the full transcript.
-- name: UpdateSessionPinUsage :exec
UPDATE router.session_pins
SET last_input_tokens        = @last_input_tokens::int,
    last_cached_read_tokens  = @last_cached_read_tokens::int,
    last_cached_write_tokens = @last_cached_write_tokens::int,
    last_output_tokens       = @last_output_tokens::int,
    last_turn_ended_at       = @last_turn_ended_at::timestamptz,
    pinned_provider          = @last_served_provider::varchar,
    has_ever_switched        = has_ever_switched
      OR @session_ever_switched::boolean
      OR (last_served_model <> '' AND last_served_model <> @last_served_model::varchar)
      OR (@prior_served_model::varchar <> '' AND @prior_served_model::varchar <> @last_served_model::varchar),
    last_served_model        = @last_served_model::varchar
WHERE session_key = @session_key::bytea
  AND role        = @role::varchar
  AND (
    routing_strategy = @expected_routing_strategy::varchar
    OR (routing_strategy = '' AND @expected_routing_strategy::varchar <> 'hmm_beta')
  );

-- Atomically increments consecutive_upstream_errors and returns the
-- new value. The turn loop calls this after a non-retryable upstream
-- 4xx on a sticky-pinned turn; the returned count drives the
-- two-strike eviction decision. Returns sql.ErrNoRows if no pin
-- exists, which the adapter maps to a no-op (pin must already be
-- evicted by another path, e.g. force-model / loop-break).
-- name: IncrementSessionPinUpstreamErrors :one
UPDATE router.session_pins
SET consecutive_upstream_errors = consecutive_upstream_errors + 1
WHERE session_key = @session_key::bytea
  AND role        = @role::varchar
  AND (
    routing_strategy = @expected_routing_strategy::varchar
    OR (routing_strategy = '' AND @expected_routing_strategy::varchar <> 'hmm_beta')
  )
RETURNING consecutive_upstream_errors;

-- Clears the two-strike counter after a successful turn. UPDATE
-- matches by (session_key, role); zero rows affected on missing pin
-- is a successful no-op like UpdateSessionPinUsage.
-- name: ResetSessionPinUpstreamErrors :exec
UPDATE router.session_pins
SET consecutive_upstream_errors = 0
WHERE session_key = @session_key::bytea
  AND role        = @role::varchar
  AND (
    routing_strategy = @expected_routing_strategy::varchar
    OR (routing_strategy = '' AND @expected_routing_strategy::varchar <> 'hmm_beta')
  )
  AND consecutive_upstream_errors > 0;

-- Atomically increments consecutive_overload_errors and returns the new
-- value. The turn loop calls this after a turn exhausts with a
-- client-visible 529 (Anthropic overloaded_error) on a sticky-pinned
-- turn; the returned count drives the two-strike provider-disable
-- decision. Returns sql.ErrNoRows if no pin exists, which the adapter
-- maps to a no-op. Separate from IncrementSessionPinUpstreamErrors
-- because a 529 is retryable in-turn and must not also trip the
-- non-retryable-4xx eviction counter.
-- name: IncrementSessionPinOverloadErrors :one
UPDATE router.session_pins
SET consecutive_overload_errors = consecutive_overload_errors + 1
WHERE session_key = @session_key::bytea
  AND role        = @role::varchar
  AND (
    routing_strategy = @expected_routing_strategy::varchar
    OR (routing_strategy = '' AND @expected_routing_strategy::varchar <> 'hmm_beta')
  )
RETURNING consecutive_overload_errors;

-- Clears the overload strike counter after a successful turn. UPDATE
-- matches by (session_key, role); zero rows affected on missing pin is
-- a successful no-op like ResetSessionPinUpstreamErrors.
-- name: ResetSessionPinOverloadErrors :exec
UPDATE router.session_pins
SET consecutive_overload_errors = 0
WHERE session_key = @session_key::bytea
  AND role        = @role::varchar
  AND (
    routing_strategy = @expected_routing_strategy::varchar
    OR (routing_strategy = '' AND @expected_routing_strategy::varchar <> 'hmm_beta')
  )
  AND consecutive_overload_errors > 0;

-- Appends a provider to disabled_providers (deduped) and resets the
-- overload strike counter in the same statement, fired once the
-- two-strike threshold is reached. disabled_providers only grows within one
-- strategy's pin lifecycle; a strategy replacement resets it with the other
-- strategy-bound evidence. There is no separate time-based cooldown.
-- name: DisableSessionPinProvider :exec
UPDATE router.session_pins
SET disabled_providers = CASE
      WHEN @provider::varchar = ANY(disabled_providers) THEN disabled_providers
      ELSE array_append(disabled_providers, @provider::varchar)
    END,
    consecutive_overload_errors = 0
WHERE session_key = @session_key::bytea
  AND role        = @role::varchar
  AND (
    routing_strategy = @expected_routing_strategy::varchar
    OR (routing_strategy = '' AND @expected_routing_strategy::varchar <> 'hmm_beta')
  );

-- Garbage-collects pins that have been expired for >24h. The 24h grace
-- means a transient Postgres outage doesn't immediately prune live pins;
-- the hourly sweep is bounded because the row count is one per active
-- session.
-- name: SweepExpiredSessionPins :exec
DELETE FROM router.session_pins
WHERE pinned_until < now() - interval '24 hours';
