BEGIN;

ALTER TABLE router.subscriber_allowance_actions
    DROP CONSTRAINT subscriber_allowance_actions_weekly_span_check,
    DROP COLUMN weekly_period_start,
    DROP COLUMN weekly_period_end;

DELETE FROM router.subscriber_allowance_periods
WHERE period_kind = 'weekly';

ALTER TABLE router.subscriber_allowance_periods
    DROP CONSTRAINT subscriber_allowance_periods_weekly_span_check;

ALTER TABLE router.subscriber_allowance_periods
    DROP CONSTRAINT subscriber_allowance_periods_period_kind_check;

ALTER TABLE router.subscriber_allowance_periods
    ADD CONSTRAINT subscriber_allowance_periods_period_kind_check
    CHECK (period_kind IN ('billing', 'six_hour'));

COMMIT;
