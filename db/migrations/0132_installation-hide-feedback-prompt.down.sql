BEGIN;

ALTER TABLE router.model_router_installations
  DROP COLUMN hide_feedback_prompt;

COMMIT;
