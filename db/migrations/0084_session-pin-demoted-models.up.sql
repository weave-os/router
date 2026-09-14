BEGIN;

-- Models struck out for this pin's session after an upstream stream failed
-- with the prelude already committed. Such a turn cannot retry or fail over,
-- so the arm is withdrawn from automatic selection for the rest of the
-- session instead of being re-picked on the next turn. Deliberately not
-- touched by UpsertSessionPin's ON CONFLICT update -- it only grows, for the
-- life of this (session_key, role) row.
ALTER TABLE router.session_pins
  ADD COLUMN demoted_models TEXT[] NOT NULL DEFAULT '{}';

COMMIT;
