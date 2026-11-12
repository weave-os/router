BEGIN;

-- No foreign key: diagnostic history survives reconnection and account removal.
CREATE TABLE router.subscription_credential_events (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 account_id UUID NOT NULL,
 subscriber_id UUID,
 provider VARCHAR NOT NULL,
 event_kind VARCHAR NOT NULL CHECK (event_kind IN ('credential_rejected', 'reconnect_required')),
 reason VARCHAR NOT NULL,
 provider_error_type VARCHAR NOT NULL DEFAULT '',
 provider_request_id VARCHAR NOT NULL DEFAULT '',
 upstream_status INTEGER NOT NULL DEFAULT 0,
 request_id VARCHAR NOT NULL DEFAULT '',
 refresh_lease_id UUID,
 token_refresh_version BIGINT NOT NULL,
 rejection_count INTEGER NOT NULL,
 first_rejected_at TIMESTAMP,
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX subscription_credential_events_account_history
 ON router.subscription_credential_events (account_id, event_kind, created_at DESC);

COMMIT;
