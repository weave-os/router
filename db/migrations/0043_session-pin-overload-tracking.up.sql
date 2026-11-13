BEGIN;

-- Counts consecutive turns that exhausted with a client-visible 529
-- (Anthropic's SSE-prelude overloaded_error) on the currently-pinned
-- provider. Distinct from consecutive_upstream_errors (0009), which only
-- counts non-retryable 4xx: a 529 is retryable in-turn (same-binding retry
-- + cross-binding failover), so it never touched that counter, and a
-- session pinned to an overloaded provider would keep re-trying it turn
-- after turn with no memory of the prior exhaustion.
ALTER TABLE router.session_pins
  ADD COLUMN consecutive_overload_errors INTEGER NOT NULL DEFAULT 0;

-- Providers struck out for this pin's session after repeated 529
-- exhaustion. Deliberately not touched by UpsertSessionPin's ON CONFLICT
-- update -- it only grows, for the life of this (session_key, role) row,
-- so a provider stays disabled until the pin itself is evicted/expires
-- (no separate time-based cooldown).
ALTER TABLE router.session_pins
  ADD COLUMN disabled_providers TEXT[] NOT NULL DEFAULT '{}';

COMMIT;
