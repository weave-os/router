BEGIN;

-- Analytics keys have no legal representation in the rolled-back schema, and
-- leaving them as 'routing' rows would silently promote a read-only credential
-- into a spend-capable one. Drop them.
DELETE FROM router.model_router_api_keys
WHERE scope = 'analytics_read';

ALTER TABLE router.model_router_api_keys
  DROP COLUMN scope;

COMMIT;
