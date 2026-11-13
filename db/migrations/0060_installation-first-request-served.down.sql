BEGIN;

ALTER TABLE router.model_router_installations
  DROP COLUMN first_request_served_at;

COMMIT;
