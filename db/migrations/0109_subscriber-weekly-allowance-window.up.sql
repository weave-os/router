BEGIN;

ALTER TABLE router.subscriber_allowance_periods
    DROP CONSTRAINT subscriber_allowance_periods_period_kind_check;

ALTER TABLE router.subscriber_allowance_periods
    ADD CONSTRAINT subscriber_allowance_periods_period_kind_check
    CHECK (period_kind IN ('billing', 'weekly', 'six_hour'));

-- A week is a fixed 168 hours, matching the Go window arithmetic: calendar days
-- would drift by an hour across a daylight-saving boundary. A weekly window is
-- shorter only as the last window of a billing period that does not divide
-- evenly into weeks.
ALTER TABLE router.subscriber_allowance_periods
    ADD CONSTRAINT subscriber_allowance_periods_weekly_span_check
    CHECK (period_kind <> 'weekly' OR period_end <= period_start + INTERVAL '168 hours');

ALTER TABLE router.subscriber_allowance_actions
    ADD COLUMN weekly_period_start TIMESTAMPTZ,
    ADD COLUMN weekly_period_end TIMESTAMPTZ;

-- Weeks run from the billing period start, so an existing action's window is
-- the 168-hour slot of its own period that its reservation fell into.
UPDATE router.subscriber_allowance_actions
SET weekly_period_start = billing_period_start
        + (FLOOR(EXTRACT(EPOCH FROM reserved_at - billing_period_start) / 604800)::double precision * INTERVAL '168 hours'),
    weekly_period_end = LEAST(
        billing_period_start
            + ((FLOOR(EXTRACT(EPOCH FROM reserved_at - billing_period_start) / 604800) + 1)::double precision * INTERVAL '168 hours'),
        billing_period_end
    );

ALTER TABLE router.subscriber_allowance_actions
    ALTER COLUMN weekly_period_start SET NOT NULL,
    ALTER COLUMN weekly_period_end SET NOT NULL,
    ADD CONSTRAINT subscriber_allowance_actions_weekly_span_check
    CHECK (
        weekly_period_start < weekly_period_end
        AND weekly_period_end <= weekly_period_start + INTERVAL '168 hours'
    );

-- Usage already booked in a week that is still open drew down the month and its
-- six-hour windows; without its weekly row that week would start from zero and
-- grant a second allowance, and an outstanding hold would settle against no
-- weekly row. The limit is provisional: the window's next reservation carries
-- the authoritative one, and admission recomputes it from the entitlement.
INSERT INTO router.subscriber_allowance_periods (
    subscriber_id,
    period_kind,
    period_start,
    period_end,
    entitlement_version,
    plan,
    limit_usd_micros,
    reserved_usd_micros,
    finalized_usd_micros
)
SELECT
    action.subscriber_id,
    'weekly',
    action.weekly_period_start,
    -- A mid-period entitlement change moves the billing end the week is clipped
    -- to, so actions sharing a start can disagree on the end: the newest
    -- entitlement wins, as it does for the period row's plan and version.
    (ARRAY_AGG(action.weekly_period_end ORDER BY action.entitlement_version DESC, action.reserved_at DESC))[1],
    MAX(action.entitlement_version),
    (ARRAY_AGG(action.plan ORDER BY action.entitlement_version DESC, action.reserved_at DESC))[1],
    0,
    COALESCE(SUM(action.reserved_usd_micros) FILTER (WHERE action.state = 'reserved'), 0),
    COALESCE(SUM(action.retail_usd_micros) FILTER (WHERE action.state = 'finalized'), 0)
FROM router.subscriber_allowance_actions AS action
WHERE action.capacity_source = 'included_router'
  AND action.state IN ('reserved', 'finalized')
  AND action.weekly_period_end > CURRENT_TIMESTAMP
GROUP BY action.subscriber_id, action.weekly_period_start;

COMMIT;
