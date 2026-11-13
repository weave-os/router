BEGIN;

ALTER TABLE router.model_router_installations
    ADD COLUMN subscription_sharing_enabled BOOLEAN NOT NULL DEFAULT TRUE;

CREATE TABLE router.model_router_subscription_account_installations (
    installation_id UUID NOT NULL REFERENCES router.model_router_installations(id) ON DELETE CASCADE,
    subscription_account_id UUID NOT NULL REFERENCES router.model_router_subscription_accounts(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (installation_id, subscription_account_id)
);
CREATE INDEX model_router_subscription_account_installations_account_idx
ON router.model_router_subscription_account_installations(subscription_account_id);

INSERT INTO router.model_router_subscription_account_installations (installation_id, subscription_account_id)
SELECT enrolling_key.installation_id, account.id
FROM router.model_router_subscription_accounts AS account
JOIN router.model_router_api_keys AS enrolling_key ON enrolling_key.id = account.api_key_id
JOIN router.model_router_installations AS installation ON installation.id = enrolling_key.installation_id
WHERE installation.deleted_at IS NULL;

-- Historical duplicate physical identities cannot safely choose an owner or
-- token. Quarantine every conflicting row instead of silently reallocating it.
UPDATE router.model_router_subscription_accounts AS account
SET enabled = FALSE, health_state = 'disabled', cooldown_until = NULL
WHERE EXISTS (
 SELECT 1 FROM router.model_router_subscription_accounts AS other
 WHERE other.provider = account.provider
 AND other.external_account_id = account.external_account_id
 AND other.id <> account.id
);
CREATE UNIQUE INDEX model_router_subscription_accounts_active_physical_identity_idx
ON router.model_router_subscription_accounts(provider, external_account_id)
WHERE enabled;

-- Durable installation registration now keeps unassigned rows addressable even
-- when their historical enrolling key is hard deleted.
ALTER TABLE router.model_router_subscription_accounts
DROP CONSTRAINT model_router_subscription_accounts_owner_present;

ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN subscription_account_id UUID,
    ADD COLUMN subscription_owner_id UUID,
    ADD COLUMN subscription_tier VARCHAR CHECK (subscription_tier IN ('personal', 'shared')),
    ADD COLUMN intended_model_family VARCHAR,
    ADD COLUMN final_model_family VARCHAR;

COMMIT;
