BEGIN;

-- Installation-shared keys have no credential subject, so they borrow the
-- installation's shared subscriptions only when an admin opts the key in.
ALTER TABLE router.model_router_api_keys
  ADD COLUMN shared_subscription_access BOOLEAN NOT NULL DEFAULT FALSE;

COMMIT;
