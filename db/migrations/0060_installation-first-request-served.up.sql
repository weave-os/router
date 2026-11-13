BEGIN;

-- First time this installation routed a request through the router. Set once
-- (WHERE IS NULL) from the key-usage path and never cleared, so it survives
-- key rotation: onboarding is gated on "this install has served traffic", and
-- deriving that from a key's last_used_at would reset the moment the only
-- active key was rotated away.
ALTER TABLE router.model_router_installations
  ADD COLUMN first_request_served_at TIMESTAMPTZ;

-- Backfill from the keys that already served traffic, soft-deleted ones
-- included: a rotated-away key still proves the install has routed. Without
-- this every existing deployment reads as never-served on upgrade and lands
-- back in first-run onboarding. MIN() because the column records the FIRST
-- request, and last_used_at on the oldest-used key is the closest bound we
-- have — the exact first-request time was never stored.
UPDATE router.model_router_installations i
SET first_request_served_at = k.first_used_at
FROM (
    SELECT installation_id, MIN(last_used_at) AS first_used_at
    FROM router.model_router_api_keys
    WHERE last_used_at IS NOT NULL
      AND scope = 'routing'
    GROUP BY installation_id
) k
WHERE i.id = k.installation_id;

COMMENT ON COLUMN router.model_router_installations.first_request_served_at IS 'Timestamp of the installation''s first routed request. Monotonic — never moves backwards or clears on key rotation.';

COMMIT;
