-- name: UpsertSubscriberEntitlement :one
-- Inserts a subscriber projection, advances a lower stored version, or returns an identical retry.
INSERT INTO router.subscriber_entitlements (
    subscriber_id,
    version,
    plan,
    status,
    billing_period_start,
    billing_period_end,
    effective_at,
    monthly_allowance_usd_micros,
    nominal_monthly_allowance_usd_micros,
    six_hour_allowance_usd_micros,
    auto_top_up_enabled,
    projected_at
) VALUES (
    @subscriber_id::uuid,
    @version::bigint,
    @plan::varchar,
    @status::varchar,
    @billing_period_start::timestamptz,
    @billing_period_end::timestamptz,
    @effective_at::timestamptz,
    @monthly_allowance_usd_micros::bigint,
    @nominal_monthly_allowance_usd_micros::bigint,
    @six_hour_allowance_usd_micros::bigint,
    @auto_top_up_enabled::boolean,
    @projected_at::timestamptz
)
ON CONFLICT (subscriber_id) DO UPDATE SET
    version = EXCLUDED.version,
    plan = EXCLUDED.plan,
    status = EXCLUDED.status,
    billing_period_start = EXCLUDED.billing_period_start,
    billing_period_end = EXCLUDED.billing_period_end,
    effective_at = EXCLUDED.effective_at,
    monthly_allowance_usd_micros = EXCLUDED.monthly_allowance_usd_micros,
    nominal_monthly_allowance_usd_micros = EXCLUDED.nominal_monthly_allowance_usd_micros,
    six_hour_allowance_usd_micros = EXCLUDED.six_hour_allowance_usd_micros,
    auto_top_up_enabled = EXCLUDED.auto_top_up_enabled,
    projected_at = EXCLUDED.projected_at,
    updated_at = CASE
        WHEN router.subscriber_entitlements.version < EXCLUDED.version THEN CURRENT_TIMESTAMP
        ELSE router.subscriber_entitlements.updated_at
    END
WHERE router.subscriber_entitlements.version < EXCLUDED.version
    OR (
        router.subscriber_entitlements.version = EXCLUDED.version
        AND ROW(
            router.subscriber_entitlements.plan,
            router.subscriber_entitlements.status,
            router.subscriber_entitlements.billing_period_start,
            router.subscriber_entitlements.billing_period_end,
            router.subscriber_entitlements.effective_at,
            router.subscriber_entitlements.monthly_allowance_usd_micros,
            router.subscriber_entitlements.nominal_monthly_allowance_usd_micros,
            router.subscriber_entitlements.six_hour_allowance_usd_micros,
            router.subscriber_entitlements.auto_top_up_enabled,
            router.subscriber_entitlements.projected_at
        ) IS NOT DISTINCT FROM ROW(
            EXCLUDED.plan,
            EXCLUDED.status,
            EXCLUDED.billing_period_start,
            EXCLUDED.billing_period_end,
            EXCLUDED.effective_at,
            EXCLUDED.monthly_allowance_usd_micros,
            EXCLUDED.nominal_monthly_allowance_usd_micros,
            EXCLUDED.six_hour_allowance_usd_micros,
            EXCLUDED.auto_top_up_enabled,
            EXCLUDED.projected_at
        )
    )
RETURNING *;

-- name: GetSubscriberEntitlement :one
-- Returns the current projected entitlement for one authenticated credential subject.
SELECT *
FROM router.subscriber_entitlements
WHERE subscriber_id = @subscriber_id::uuid;
