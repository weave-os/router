BEGIN;

-- Month-scoped one-off spend grants: an approved amount added on top of an
-- engineer's base monthly cap for one UTC calendar month only. The request-path
-- gate adds this month's grants to the effective limit; grants never carry over.
CREATE TABLE router.model_router_user_spend_grants (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    router_user_id     UUID NOT NULL
        REFERENCES router.model_router_users (id) ON DELETE CASCADE,
    month              DATE NOT NULL,
    amount_usd_micros  BIGINT NOT NULL CHECK (amount_usd_micros > 0),
    reason             TEXT,
    approved_by        TEXT,
    created_by         TEXT NOT NULL,
    idempotency_key    TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A retried grant request with the same key is a no-op rather than a second grant.
CREATE UNIQUE INDEX model_router_user_spend_grants_idempotency_idx
    ON router.model_router_user_spend_grants (router_user_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- Serves the gate's "this user's grants for this month" sum.
CREATE INDEX model_router_user_spend_grants_user_month_idx
    ON router.model_router_user_spend_grants (router_user_id, month);

-- Append-only audit trail of spend-limit configuration changes. The Weave
-- backend writes rows directly; the router only owns the schema. A null
-- router_user_id marks an org-level change.
CREATE TABLE router.spend_limit_changes (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id  VARCHAR(36) NOT NULL,
    router_user_id   UUID,
    kind             TEXT NOT NULL CHECK (kind IN (
        'org_limits',
        'user_override',
        'user_override_cleared',
        'grant'
    )),
    old_value        JSONB,
    new_value        JSONB NOT NULL,
    reason           TEXT,
    approved_by      TEXT,
    actor            TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX spend_limit_changes_org_created_idx
    ON router.spend_limit_changes (organization_id, created_at DESC);

COMMIT;
