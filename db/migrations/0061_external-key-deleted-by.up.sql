BEGIN;

-- Attribute who soft-deleted a BYOK provider key or who replaced it during an
-- upsert. Complements the existing created_by. NULL deliberately mirrors the
-- created_by convention: rows soft-deleted internally by the router (rather
-- than by an admin in Weave settings) have no account to record.
ALTER TABLE router.model_router_external_api_keys
  ADD COLUMN deleted_by VARCHAR(36);

COMMENT ON COLUMN router.model_router_external_api_keys.deleted_by IS 'Weave account that soft-deleted or replaced this key; NULL when the router deleted it internally or attribution predates the column';

COMMIT;
