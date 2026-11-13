BEGIN;

ALTER TABLE router.model_router_api_keys DROP COLUMN paid_fallback_on_subscription_refusal;

COMMIT;
