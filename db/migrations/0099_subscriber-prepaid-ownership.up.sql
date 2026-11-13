BEGIN;

-- Prepaid credit owned by an individual Max/Boost subscriber rather than an
-- organization. The owner is the stable credential subject, so two subscribers
-- inside the same organization keep separate books and neither can spend the
-- other's funds. Included subscription allowance is metered in
-- router.subscriber_allowance_periods and is deliberately not stored here:
-- allowance is capacity the plan already bought, prepaid credit is money.
CREATE TABLE router.subscriber_credit_balance (
    subscriber_id           UUID        PRIMARY KEY REFERENCES router.credential_subjects(id) ON DELETE CASCADE,
    balance_usd_micros      BIGINT      NOT NULL DEFAULT 0,
    low_balance_notified_at TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Append-only history of the subscriber's grants, debits, and adjustments,
-- mirroring router.organization_credit_ledger's entry vocabulary so both owner
-- kinds read the same way. delta_usd_micros is positive for grants and
-- negative for debits; notional_cost_micros records the would-be charge even
-- when the turn itself debited nothing.
CREATE TABLE router.subscriber_credit_ledger (
    id                       UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_id            UUID        NOT NULL REFERENCES router.credential_subjects(id) ON DELETE CASCADE,
    delta_usd_micros         BIGINT      NOT NULL,
    notional_cost_micros     BIGINT      NOT NULL DEFAULT 0,
    balance_after_micros     BIGINT      NOT NULL,
    entry_type               VARCHAR(32) NOT NULL CHECK (entry_type IN ('topup','inference','refund','adjustment')),
    stripe_payment_intent_id VARCHAR(255),
    router_request_id        VARCHAR(64),
    router_model             VARCHAR(128),
    memo                     TEXT,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX subscriber_credit_ledger_subscriber_created_at_idx
    ON router.subscriber_credit_ledger (subscriber_id, created_at DESC);

-- Stripe redelivers the same completed checkout event; the partial unique
-- index gives the subscriber's own book idempotency without blocking the
-- inference rows that carry no payment intent. Scoping it to this table keeps
-- subscriber top-ups from colliding with organization ones.
CREATE UNIQUE INDEX subscriber_credit_ledger_stripe_pi_idx
    ON router.subscriber_credit_ledger (stripe_payment_intent_id)
    WHERE stripe_payment_intent_id IS NOT NULL;

COMMIT;
