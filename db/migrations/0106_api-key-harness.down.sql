BEGIN;

ALTER TABLE router.model_router_api_keys
    DROP COLUMN harness;

COMMIT;
