BEGIN;

ALTER TABLE router.model_router_installations
  DROP COLUMN content_capture_mode;

COMMIT;
