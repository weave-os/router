BEGIN;
ALTER TABLE router.model_router_inference_attempts
 DROP COLUMN capacity_observed_at,
 DROP COLUMN selection_policy_sha256,
 DROP COLUMN funding_pool_id,
 DROP COLUMN capacity_source,
 DROP COLUMN credential_source,
 DROP COLUMN subscription_account_id;
COMMIT;
