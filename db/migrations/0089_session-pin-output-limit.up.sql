BEGIN;

-- Match last_turn_ended_at to bind confirmed cap evidence to the same usage
-- write. Older writers leave this marker behind, so a mismatch invalidates it.
-- NULL leaves legacy rows unconfirmed without a token-count backfill.
ALTER TABLE router.session_pins
  ADD COLUMN last_output_limit_at TIMESTAMPTZ;

COMMIT;
