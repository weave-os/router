BEGIN;

CREATE TABLE router.subscriber_entitlements (
    subscriber_id UUID PRIMARY KEY REFERENCES router.credential_subjects(id) ON DELETE CASCADE,
    version BIGINT NOT NULL CHECK (version > 0),
    plan VARCHAR NOT NULL CHECK (plan IN ('max', 'boost')),
    status VARCHAR NOT NULL CHECK (status IN ('checkout_pending', 'active', 'past_due', 'canceled', 'ended')),
    billing_period_start TIMESTAMPTZ NOT NULL,
    billing_period_end TIMESTAMPTZ NOT NULL,
    effective_at TIMESTAMPTZ NOT NULL,
    monthly_allowance_usd_micros BIGINT NOT NULL CHECK (monthly_allowance_usd_micros >= 0),
    nominal_monthly_allowance_usd_micros BIGINT NOT NULL CHECK (nominal_monthly_allowance_usd_micros >= 0),
    six_hour_allowance_usd_micros BIGINT NOT NULL CHECK (six_hour_allowance_usd_micros >= 0),
    auto_top_up_enabled BOOLEAN NOT NULL,
    projected_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (billing_period_start < billing_period_end)
);

CREATE TABLE router.subscriber_allowance_periods (
    subscriber_id UUID NOT NULL REFERENCES router.credential_subjects(id) ON DELETE CASCADE,
    period_kind VARCHAR NOT NULL CHECK (period_kind IN ('billing', 'six_hour')),
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    entitlement_version BIGINT NOT NULL CHECK (entitlement_version > 0),
    plan VARCHAR NOT NULL CHECK (plan IN ('max', 'boost')),
    limit_usd_micros BIGINT NOT NULL CHECK (limit_usd_micros >= 0),
    reserved_usd_micros BIGINT NOT NULL DEFAULT 0 CHECK (reserved_usd_micros >= 0),
    finalized_usd_micros BIGINT NOT NULL DEFAULT 0 CHECK (finalized_usd_micros >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (subscriber_id, period_kind, period_start),
    CHECK (period_start < period_end),
    CHECK (
        period_kind <> 'six_hour'
        OR (
            period_end = period_start + INTERVAL '6 hours'
            AND period_start AT TIME ZONE 'UTC' = date_trunc('hour', period_start AT TIME ZONE 'UTC')
            AND EXTRACT(HOUR FROM period_start AT TIME ZONE 'UTC') IN (0, 6, 12, 18)
        )
    )
);

CREATE TABLE router.subscriber_allowance_actions (
    action_id VARCHAR PRIMARY KEY,
    router_request_id VARCHAR NOT NULL,
    subscriber_id UUID NOT NULL REFERENCES router.credential_subjects(id) ON DELETE CASCADE,
    entitlement_version BIGINT NOT NULL CHECK (entitlement_version > 0),
    plan VARCHAR NOT NULL CHECK (plan IN ('max', 'boost')),
    billing_period_start TIMESTAMPTZ NOT NULL,
    billing_period_end TIMESTAMPTZ NOT NULL,
    six_hour_period_start TIMESTAMPTZ NOT NULL,
    six_hour_period_end TIMESTAMPTZ NOT NULL,
    api_key_id UUID NOT NULL,
    client_session_id VARCHAR,
    requested_model VARCHAR NOT NULL,
    served_model VARCHAR,
    reserved_usd_micros BIGINT NOT NULL CHECK (reserved_usd_micros >= 0),
    retail_usd_micros BIGINT CHECK (retail_usd_micros >= 0),
    capacity_source VARCHAR NOT NULL CHECK (
        capacity_source IN ('included_router', 'linked_claude', 'linked_codex', 'prepaid', 'billing_override')
    ),
    state VARCHAR NOT NULL CHECK (state IN ('reserved', 'finalized', 'released')),
    reserved_at TIMESTAMPTZ NOT NULL,
    finalized_at TIMESTAMPTZ,
    released_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (billing_period_start < billing_period_end),
    CHECK (six_hour_period_end = six_hour_period_start + INTERVAL '6 hours'),
    CHECK (six_hour_period_start AT TIME ZONE 'UTC' = date_trunc('hour', six_hour_period_start AT TIME ZONE 'UTC')),
    CHECK (EXTRACT(HOUR FROM six_hour_period_start AT TIME ZONE 'UTC') IN (0, 6, 12, 18)),
    CHECK (finalized_at IS NULL OR finalized_at >= reserved_at),
    CHECK (released_at IS NULL OR released_at >= reserved_at),
    CHECK (
        (state = 'reserved' AND served_model IS NULL AND retail_usd_micros IS NULL AND finalized_at IS NULL AND released_at IS NULL)
        OR (state = 'finalized' AND served_model IS NOT NULL AND retail_usd_micros IS NOT NULL AND finalized_at IS NOT NULL AND released_at IS NULL)
        OR (state = 'released' AND served_model IS NULL AND retail_usd_micros IS NULL AND finalized_at IS NULL AND released_at IS NOT NULL)
    )
);

CREATE INDEX idx_subscriber_allowance_actions_request
    ON router.subscriber_allowance_actions (subscriber_id, router_request_id);

CREATE INDEX idx_subscriber_allowance_actions_period
    ON router.subscriber_allowance_actions (subscriber_id, billing_period_start, six_hour_period_start);

COMMIT;
