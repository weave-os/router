BEGIN;

-- Models that failed and were rescued during the current agent turn. They stay
-- out of the session until the next real user prompt clears the list.
ALTER TABLE router.session_pins
  ADD COLUMN turn_strikes TEXT[] NOT NULL DEFAULT '{}';

COMMIT;
