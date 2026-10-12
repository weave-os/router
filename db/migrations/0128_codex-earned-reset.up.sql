BEGIN;

-- A subscriber may have sessions on multiple installations and router replicas.
-- Retain uncertain redemptions so all retries use the same credit and request ID.
CREATE TABLE router.codex_usage_resets (
    subscriber_id UUID PRIMARY KEY REFERENCES router.credential_subjects(id) ON DELETE CASCADE,
    lease_id UUID,
    lease_until TIMESTAMP,
    account_id UUID,
    credit_id TEXT,
    request_id UUID,
    CHECK ((lease_id IS NULL) = (lease_until IS NULL)),
    CHECK ((account_id IS NULL) = (credit_id IS NULL) AND (credit_id IS NULL) = (request_id IS NULL))
);

COMMIT;
