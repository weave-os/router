BEGIN;

-- Linked Claude/Codex accounts belong to the subscriber, not to the key that
-- happened to enroll them: a rotated key or a second harness key must serve the
-- same pool. api_key_id stays as enrollment attribution and as the ownership
-- predicate for legacy rows that could not be attributed to a subscriber.
ALTER TABLE router.model_router_subscription_accounts
    ADD COLUMN subscriber_id UUID REFERENCES router.credential_subjects(id) ON DELETE CASCADE;

ALTER TABLE router.model_router_subscription_accounts
    ALTER COLUMN api_key_id DROP NOT NULL;

ALTER TABLE router.model_router_subscription_accounts
    DROP CONSTRAINT model_router_subscription_accounts_api_key_id_fkey;

-- Deleting the enrolling key must not delete a subscriber's linked account; it
-- only forfeits the audit attribution.
ALTER TABLE router.model_router_subscription_accounts
    ADD CONSTRAINT model_router_subscription_accounts_api_key_id_fkey
    FOREIGN KEY (api_key_id) REFERENCES router.model_router_api_keys(id) ON DELETE SET NULL;

-- No row may end up addressable by nobody while still holding an encrypted
-- refresh token. Keys are soft-deleted in the product, so this only refuses a
-- manual hard delete of a key that still owns unmigrated legacy rows.
ALTER TABLE router.model_router_subscription_accounts
    ADD CONSTRAINT model_router_subscription_accounts_owner_present
    CHECK (subscriber_id IS NOT NULL OR api_key_id IS NOT NULL);

-- Backfill only unambiguous attribution: the enrolling key carries a credential
-- subject, and no other row would collapse onto the same
-- (subscriber, provider, external account). Duplicate candidates keep legacy
-- api_key ownership rather than being merged into one subscriber account with a
-- guessed set of tokens.
UPDATE router.model_router_subscription_accounts AS account
SET subscriber_id = enrolling_key.credential_subject_id,
    updated_at = CURRENT_TIMESTAMP
FROM router.model_router_api_keys AS enrolling_key
WHERE enrolling_key.id = account.api_key_id
  AND enrolling_key.credential_subject_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1
    FROM router.model_router_subscription_accounts AS sibling
    JOIN router.model_router_api_keys AS sibling_key ON sibling_key.id = sibling.api_key_id
    WHERE sibling.id <> account.id
      AND sibling.provider = account.provider
      AND sibling.external_account_id = account.external_account_id
      AND sibling_key.credential_subject_id = enrolling_key.credential_subject_id
  );

CREATE UNIQUE INDEX model_router_subscription_accounts_subscriber_account_idx
    ON router.model_router_subscription_accounts(subscriber_id, provider, external_account_id)
    WHERE subscriber_id IS NOT NULL;

CREATE INDEX model_router_subscription_accounts_subscriber_idx
    ON router.model_router_subscription_accounts(subscriber_id, provider)
    WHERE subscriber_id IS NOT NULL;

COMMIT;
