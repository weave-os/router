BEGIN;

-- First time this installation routed a request through the router. Set once
-- (WHERE IS NULL) from the key-usage path and never cleared, so it survives
-- key rotation: onboarding is gated on "this install has served traffic", and
-- deriving that from a key's last_used_at would reset the moment the only
-- active key was rotated away.
ALTER TABLE router.model_router_installations
  ADD COLUMN first_request_served_at TIMESTAMPTZ;

COMMENT ON COLUMN router.model_router_installations.first_request_served_at IS 'Timestamp of the installation''s first routed request. Monotonic — never moves backwards or clears on key rotation.';

COMMIT;
