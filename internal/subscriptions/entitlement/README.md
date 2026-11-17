# internal/subscriptions/entitlement

Individual Router subscriptions (Max and Boost): the projected entitlement,
the included-allowance windows, admission against them and settlement of each
served action's retail cost.

## Product boundary

Each plan sells a hard model-eligibility boundary: Max is open-source-only,
Boost is unrestricted. The boundary is part of the product, so it applies to
every turn an individual subscriber makes, including turns their spent
allowance pushes onto prepaid or organization capacity. That is why product
scope (`WithProductScope`) is stamped separately from allowance coverage
(`WithCoverage`). A plan added without a boundary refuses every model
(`TestPlanBoundariesCoverEveryPlan`).

## Enforcement windows

Three windows are enforced together, all derived from the plan's nominal
monthly allowance so a mid-period plan change grants the target plan's whole
window immediately while usage already booked keeps counting:

- **Billing period**: the projected allowance, prorated by the commerce side
  across the entitlement segments of the period.
- **Weekly**: seven-day windows anchored to the billing period start, the last
  one short when the period is not a whole number of weeks. Weekly caps sum back
  to the nominal allowance, so they pace the period: a subscriber can burst
  within a week but cannot pull a later week forward.
- **Six-hour**: fixed UTC windows at 00:00, 06:00, 12:00 and 18:00 shared by all
  subscribers. Partial windows at the period edges count as whole ones. The cap
  is the even share times `SixHourBurstFactor` (never more than the whole
  allowance), so one heavy session can draw on capacity unused elsewhere.

Integer remainders go to the earliest windows one micro each, so a window's cap
does not depend on when it is read.

## Admission and settlement

`Admit` reports a missing, inactive or expired entitlement as not subscribed
rather than exhausted: those requests keep the organization or prepaid paths
that already gate them, and a stale projection never hands out free usage. Read
failures are returned, never treated as headroom. An exhausted admission names
the longest spent window (billing, then weekly, then six-hour) because the
remediation differs, and reports when every spent window has reset.

Admission is the only gate. Held cost counts as consumed, so turns in flight
when a window fills can overshoot it by at most their own cost, and the next
admission rejects. Coverage captures the windows and clock seen at admission;
settlement books against those even when a turn serves past a window boundary or
a projection lands mid-flight.

`Settle` files the hold and its finalization back to back under one action ID
(each upstream action of a request, such as a handover summary, compaction or
sibling failover, gets its own ID from `NextActionID`). Duplicate deliveries are
absorbed by the repository. A hold whose finalization fails is left standing:
it already counts the turn's cost, so the subscriber is metered correctly and
only the served-model audit detail is lost. That failure is
`ErrAllowanceHeldUnsettled`, so a caller falling back to another book does not
charge the turn twice.
