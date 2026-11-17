BEGIN;
ALTER TABLE router.model_router_inference_attempts
 ADD COLUMN subscription_account_id VARCHAR,
 ADD COLUMN credential_source VARCHAR,
 ADD COLUMN capacity_source VARCHAR,
 ADD COLUMN funding_pool_id VARCHAR,
 ADD COLUMN selection_policy_sha256 VARCHAR,
 ADD COLUMN capacity_observed_at TIMESTAMPTZ;
COMMIT;
