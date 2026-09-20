-- name: ReserveSubscriberAllowance :one
-- Records an allowance hold for one action and accrues it against the
-- subscriber's billing and six-hour windows. A redelivered action_id returns
-- the stored action without accruing a second hold. Only included-Router
-- capacity draws down the windows; a turn served on a linked or prepaid source
-- is audited without consuming the subscription allowance.
WITH reserved AS (
    INSERT INTO router.subscriber_allowance_actions (
        action_id,
        router_request_id,
        subscriber_id,
        entitlement_version,
        plan,
        billing_period_start,
        billing_period_end,
        six_hour_period_start,
        six_hour_period_end,
        api_key_id,
        client_session_id,
        requested_model,
        reserved_usd_micros,
        capacity_source,
        state,
        reserved_at
    ) VALUES (
        @action_id::varchar,
        @router_request_id::varchar,
        @subscriber_id::uuid,
        @entitlement_version::bigint,
        @plan::varchar,
        @billing_period_start::timestamptz,
        @billing_period_end::timestamptz,
        @six_hour_period_start::timestamptz,
        @six_hour_period_end::timestamptz,
        @api_key_id::uuid,
        sqlc.narg('client_session_id')::varchar,
        @requested_model::varchar,
        @reserved_usd_micros::bigint,
        @capacity_source::varchar,
        'reserved',
        @reserved_at::timestamptz
    )
    ON CONFLICT (action_id) DO NOTHING
    RETURNING *
), billing_window AS (
    INSERT INTO router.subscriber_allowance_periods (
        subscriber_id,
        period_kind,
        period_start,
        period_end,
        entitlement_version,
        plan,
        limit_usd_micros,
        reserved_usd_micros
    )
    SELECT
        reserved.subscriber_id,
        'billing',
        reserved.billing_period_start,
        reserved.billing_period_end,
        reserved.entitlement_version,
        reserved.plan,
        @billing_limit_usd_micros::bigint,
        reserved.reserved_usd_micros
    FROM reserved
    WHERE reserved.capacity_source = 'included_router'
    ON CONFLICT (subscriber_id, period_kind, period_start) DO UPDATE SET
        period_end = CASE
            WHEN EXCLUDED.entitlement_version >= router.subscriber_allowance_periods.entitlement_version THEN EXCLUDED.period_end
            ELSE router.subscriber_allowance_periods.period_end
        END,
        entitlement_version = GREATEST(router.subscriber_allowance_periods.entitlement_version, EXCLUDED.entitlement_version),
        plan = CASE
            WHEN EXCLUDED.entitlement_version >= router.subscriber_allowance_periods.entitlement_version THEN EXCLUDED.plan
            ELSE router.subscriber_allowance_periods.plan
        END,
        limit_usd_micros = CASE
            WHEN EXCLUDED.entitlement_version >= router.subscriber_allowance_periods.entitlement_version THEN EXCLUDED.limit_usd_micros
            ELSE router.subscriber_allowance_periods.limit_usd_micros
        END,
        reserved_usd_micros = router.subscriber_allowance_periods.reserved_usd_micros + EXCLUDED.reserved_usd_micros,
        updated_at = CURRENT_TIMESTAMP
), six_hour_window AS (
    INSERT INTO router.subscriber_allowance_periods (
        subscriber_id,
        period_kind,
        period_start,
        period_end,
        entitlement_version,
        plan,
        limit_usd_micros,
        reserved_usd_micros
    )
    SELECT
        reserved.subscriber_id,
        'six_hour',
        reserved.six_hour_period_start,
        reserved.six_hour_period_end,
        reserved.entitlement_version,
        reserved.plan,
        @six_hour_limit_usd_micros::bigint,
        reserved.reserved_usd_micros
    FROM reserved
    WHERE reserved.capacity_source = 'included_router'
    ON CONFLICT (subscriber_id, period_kind, period_start) DO UPDATE SET
        period_end = CASE
            WHEN EXCLUDED.entitlement_version >= router.subscriber_allowance_periods.entitlement_version THEN EXCLUDED.period_end
            ELSE router.subscriber_allowance_periods.period_end
        END,
        entitlement_version = GREATEST(router.subscriber_allowance_periods.entitlement_version, EXCLUDED.entitlement_version),
        plan = CASE
            WHEN EXCLUDED.entitlement_version >= router.subscriber_allowance_periods.entitlement_version THEN EXCLUDED.plan
            ELSE router.subscriber_allowance_periods.plan
        END,
        limit_usd_micros = CASE
            WHEN EXCLUDED.entitlement_version >= router.subscriber_allowance_periods.entitlement_version THEN EXCLUDED.limit_usd_micros
            ELSE router.subscriber_allowance_periods.limit_usd_micros
        END,
        reserved_usd_micros = router.subscriber_allowance_periods.reserved_usd_micros + EXCLUDED.reserved_usd_micros,
        updated_at = CURRENT_TIMESTAMP
)
SELECT * FROM reserved
UNION ALL
SELECT * FROM router.subscriber_allowance_actions
WHERE action_id = @action_id::varchar
  AND NOT EXISTS (SELECT 1 FROM reserved);

-- name: FinalizeSubscriberAllowance :one
-- Settles a reserved action at its actual retail cost, releasing the hold and
-- accruing the cost against both windows. Matching on the reserved capacity
-- source keeps a turn that ended up served elsewhere from silently settling
-- against the subscription allowance. A redelivered finalization returns the
-- stored action unchanged.
WITH finalized AS (
    UPDATE router.subscriber_allowance_actions
    SET state = 'finalized',
        served_model = @served_model::varchar,
        retail_usd_micros = @retail_usd_micros::bigint,
        finalized_at = @finalized_at::timestamptz,
        updated_at = CURRENT_TIMESTAMP
    WHERE action_id = @action_id::varchar
      AND state = 'reserved'
      AND capacity_source = @capacity_source::varchar
    RETURNING *
), billing_window AS (
    UPDATE router.subscriber_allowance_periods
    SET reserved_usd_micros = GREATEST(router.subscriber_allowance_periods.reserved_usd_micros - finalized.reserved_usd_micros, 0),
        finalized_usd_micros = router.subscriber_allowance_periods.finalized_usd_micros + finalized.retail_usd_micros,
        updated_at = CURRENT_TIMESTAMP
    FROM finalized
    WHERE router.subscriber_allowance_periods.subscriber_id = finalized.subscriber_id
      AND router.subscriber_allowance_periods.period_kind = 'billing'
      AND router.subscriber_allowance_periods.period_start = finalized.billing_period_start
      AND finalized.capacity_source = 'included_router'
), six_hour_window AS (
    UPDATE router.subscriber_allowance_periods
    SET reserved_usd_micros = GREATEST(router.subscriber_allowance_periods.reserved_usd_micros - finalized.reserved_usd_micros, 0),
        finalized_usd_micros = router.subscriber_allowance_periods.finalized_usd_micros + finalized.retail_usd_micros,
        updated_at = CURRENT_TIMESTAMP
    FROM finalized
    WHERE router.subscriber_allowance_periods.subscriber_id = finalized.subscriber_id
      AND router.subscriber_allowance_periods.period_kind = 'six_hour'
      AND router.subscriber_allowance_periods.period_start = finalized.six_hour_period_start
      AND finalized.capacity_source = 'included_router'
)
SELECT * FROM finalized
UNION ALL
SELECT * FROM router.subscriber_allowance_actions
WHERE action_id = @action_id::varchar
  AND NOT EXISTS (SELECT 1 FROM finalized);

-- name: ReleaseSubscriberAllowance :one
-- Returns a non-billable hold to both windows. A redelivered release returns
-- the stored action unchanged.
WITH released AS (
    UPDATE router.subscriber_allowance_actions
    SET state = 'released',
        released_at = @released_at::timestamptz,
        updated_at = CURRENT_TIMESTAMP
    WHERE action_id = @action_id::varchar
      AND state = 'reserved'
    RETURNING *
), billing_window AS (
    UPDATE router.subscriber_allowance_periods
    SET reserved_usd_micros = GREATEST(router.subscriber_allowance_periods.reserved_usd_micros - released.reserved_usd_micros, 0),
        updated_at = CURRENT_TIMESTAMP
    FROM released
    WHERE router.subscriber_allowance_periods.subscriber_id = released.subscriber_id
      AND router.subscriber_allowance_periods.period_kind = 'billing'
      AND router.subscriber_allowance_periods.period_start = released.billing_period_start
      AND released.capacity_source = 'included_router'
), six_hour_window AS (
    UPDATE router.subscriber_allowance_periods
    SET reserved_usd_micros = GREATEST(router.subscriber_allowance_periods.reserved_usd_micros - released.reserved_usd_micros, 0),
        updated_at = CURRENT_TIMESTAMP
    FROM released
    WHERE router.subscriber_allowance_periods.subscriber_id = released.subscriber_id
      AND router.subscriber_allowance_periods.period_kind = 'six_hour'
      AND router.subscriber_allowance_periods.period_start = released.six_hour_period_start
      AND released.capacity_source = 'included_router'
)
SELECT * FROM released
UNION ALL
SELECT * FROM router.subscriber_allowance_actions
WHERE action_id = @action_id::varchar
  AND NOT EXISTS (SELECT 1 FROM released);

-- name: GetSubscriberAllowanceAction :one
-- Reads one stored allowance action. Callers use it after a write statement
-- declined to transition a row, because that statement's snapshot cannot see a
-- concurrently committed action.
SELECT *
FROM router.subscriber_allowance_actions
WHERE action_id = @action_id::varchar;

-- name: ListSubscriberAllowanceWindows :many
-- Reads the consumed amounts for the two enforcement windows covering one
-- request. A window with no activity yet has no row.
SELECT *
FROM router.subscriber_allowance_periods
WHERE subscriber_id = @subscriber_id::uuid
  AND (
      (period_kind = 'billing' AND period_start = @billing_period_start::timestamptz)
      OR (period_kind = 'six_hour' AND period_start = @six_hour_period_start::timestamptz)
  );
