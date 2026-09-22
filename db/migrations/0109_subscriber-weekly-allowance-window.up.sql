BEGIN;

ALTER TABLE router.subscriber_allowance_periods
    DROP CONSTRAINT subscriber_allowance_periods_period_kind_check;

ALTER TABLE router.subscriber_allowance_periods
    ADD CONSTRAINT subscriber_allowance_periods_period_kind_check
    CHECK (period_kind IN ('billing', 'weekly', 'six_hour'));

-- A weekly window is shorter than seven days only as the last window of a
-- billing period that does not divide evenly into weeks.
ALTER TABLE router.subscriber_allowance_periods
    ADD CONSTRAINT subscriber_allowance_periods_weekly_span_check
    CHECK (period_kind <> 'weekly' OR period_end <= period_start + INTERVAL '7 days');

ALTER TABLE router.subscriber_allowance_actions
    ADD COLUMN weekly_period_start TIMESTAMPTZ,
    ADD COLUMN weekly_period_end TIMESTAMPTZ;

-- Weeks run from the billing period start, so an existing action's window is
-- the seven-day slot of its own period that its reservation fell into.
UPDATE router.subscriber_allowance_actions
SET weekly_period_start = billing_period_start
        + (FLOOR(EXTRACT(EPOCH FROM reserved_at - billing_period_start) / 604800)::double precision * INTERVAL '7 days'),
    weekly_period_end = LEAST(
        billing_period_start
            + ((FLOOR(EXTRACT(EPOCH FROM reserved_at - billing_period_start) / 604800) + 1)::double precision * INTERVAL '7 days'),
        billing_period_end
    );

ALTER TABLE router.subscriber_allowance_actions
    ALTER COLUMN weekly_period_start SET NOT NULL,
    ALTER COLUMN weekly_period_end SET NOT NULL,
    ADD CONSTRAINT subscriber_allowance_actions_weekly_span_check
    CHECK (
        weekly_period_start < weekly_period_end
        AND weekly_period_end <= weekly_period_start + INTERVAL '7 days'
    );

COMMIT;
