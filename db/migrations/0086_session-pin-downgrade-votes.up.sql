BEGIN;

-- Counts consecutive turns on which the HMM authoritative-per-turn classifier
-- proposed a cheaper model than the pinned one while downgrade hysteresis held
-- the pin. Reset to 0 whenever the classifier confirms the pin, proposes an
-- upgrade, or the downgrade is finally applied, so the count always describes
-- an unbroken run of cheaper-than-pin votes.
ALTER TABLE router.session_pins
  ADD COLUMN consecutive_downgrade_votes INTEGER NOT NULL DEFAULT 0;

COMMIT;
