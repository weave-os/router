BEGIN;

ALTER TABLE router.model_router_api_keys DROP COLUMN shared_subscription_access;

COMMIT;
