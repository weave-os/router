BEGIN;

-- Reasoning effort carried by a pin (the `:level` suffix of /force-model or
-- x-weave-force-model). Empty means the model's default effort. Upsert always
-- takes the incoming value so a re-force without a level clears it.
ALTER TABLE router.session_pins
  ADD COLUMN pinned_effort VARCHAR NOT NULL DEFAULT '';

COMMIT;
