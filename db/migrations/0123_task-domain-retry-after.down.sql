BEGIN;

ALTER TABLE router.task_domain_profiles DROP COLUMN retry_after;

COMMIT;
