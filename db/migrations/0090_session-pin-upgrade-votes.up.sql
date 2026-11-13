BEGIN;

-- Counts consecutive expensive same-group HMM upgrade proposals that the
-- evidence policy held. Reset whenever the pin is rewritten, a switch is
-- served, or the proposal is no longer an ambiguous same-group upgrade.
ALTER TABLE router.session_pins
  ADD COLUMN consecutive_upgrade_votes INTEGER NOT NULL DEFAULT 0;

COMMIT;
