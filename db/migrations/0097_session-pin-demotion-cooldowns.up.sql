BEGIN;

-- Models withdrawn from this pin's session only until an instant, keyed by
-- model with an RFC 3339 timestamptz value: {"claude-opus-4-1": "2026-..."}.
-- Written when the primary arm of a rescued turn was rate-limited upstream
-- (429): throttling is transient, so unlike demoted_models the arm returns
-- to automatic selection once its cooldown elapses. Reset alongside
-- demoted_models when another strategy takes the row over; otherwise only
-- ever overwritten per model, never cleared, because a stale entry is
-- harmless once its instant has passed.
ALTER TABLE router.session_pins
  ADD COLUMN demotion_cooldowns JSONB NOT NULL DEFAULT '{}'::jsonb;

COMMIT;
