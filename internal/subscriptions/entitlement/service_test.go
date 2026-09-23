package entitlement_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSubscriber = entitlement.SubscriberID("11111111-1111-1111-1111-111111111111")
	testAPIKeyID   = "22222222-2222-2222-2222-222222222222"
	maxMonthly     = int64(50_000_000)
	// The March 2026 period spans 124 fixed windows and 50_000_000 leaves a
	// remainder of 100, so each of the first 100 windows carries one extra
	// micro before the burst factor multiplies it.
	maxSixHour = 4 * int64(403_226)
	// The period holds five weekly windows, the last one three days long.
	maxWeekly = int64(10_000_000)
)

var testNow = time.Date(2026, 3, 14, 9, 30, 0, 0, time.UTC)

// fakeEntitlements answers the projection read with a fixed entitlement or error.
type fakeEntitlements struct {
	current entitlement.Entitlement
	found   bool
	err     error
}

func (f *fakeEntitlements) Project(context.Context, entitlement.Entitlement) error { return nil }

func (f *fakeEntitlements) Get(context.Context, entitlement.SubscriberID) (entitlement.Entitlement, error) {
	if f.err != nil {
		return entitlement.Entitlement{}, f.err
	}
	if !f.found {
		return entitlement.Entitlement{}, entitlement.ErrEntitlementNotFound
	}
	return f.current, nil
}

// fakeAllowances records the accounting commands the service issues and answers
// usage reads from the consumed amounts each scenario sets.
type fakeAllowances struct {
	billingReserved int64
	billingFinal    int64
	weeklyReserved  int64
	weeklyFinal     int64
	sixHourReserved int64
	sixHourFinal    int64
	usageErr        error

	reserveErr   error
	exhausted    entitlement.PeriodKind
	finalizeErr  error
	storedState  entitlement.ActionState
	reservations []entitlement.Reservation
	finalizes    []entitlement.Finalization
	releases     []entitlement.Release
}

func (f *fakeAllowances) Reserve(_ context.Context, reservation entitlement.Reservation) (entitlement.Action, error) {
	f.reservations = append(f.reservations, reservation)
	if f.reserveErr != nil {
		return entitlement.Action{}, f.reserveErr
	}
	state := f.storedState
	if state == "" {
		state = entitlement.ActionStateReserved
	}
	return entitlement.Action{Reservation: reservation, State: state}, nil
}

func (f *fakeAllowances) ReserveWithinLimits(ctx context.Context, reservation entitlement.Reservation) (entitlement.Action, error) {
	if f.exhausted != "" {
		f.reservations = append(f.reservations, reservation)
		return entitlement.Action{}, entitlement.ExhaustedError{Period: f.exhausted}
	}
	return f.Reserve(ctx, reservation)
}

func (f *fakeAllowances) Finalize(_ context.Context, finalization entitlement.Finalization) (entitlement.Action, error) {
	f.finalizes = append(f.finalizes, finalization)
	if f.finalizeErr != nil {
		return entitlement.Action{}, f.finalizeErr
	}
	return entitlement.Action{State: entitlement.ActionStateFinalized}, nil
}

func (f *fakeAllowances) Release(_ context.Context, release entitlement.Release) (entitlement.Action, error) {
	f.releases = append(f.releases, release)
	return entitlement.Action{State: entitlement.ActionStateReleased}, nil
}

func (f *fakeAllowances) Usage(_ context.Context, _ entitlement.SubscriberID, billing, weekly, sixHour entitlement.Period) (entitlement.Usage, error) {
	if f.usageErr != nil {
		return entitlement.Usage{}, f.usageErr
	}
	return entitlement.Usage{
		Billing: entitlement.WindowUsage{
			Period:             billing,
			ReservedUsdMicros:  f.billingReserved,
			FinalizedUsdMicros: f.billingFinal,
		},
		Weekly: entitlement.WindowUsage{
			Period:             weekly,
			ReservedUsdMicros:  f.weeklyReserved,
			FinalizedUsdMicros: f.weeklyFinal,
		},
		SixHour: entitlement.WindowUsage{
			Period:             sixHour,
			ReservedUsdMicros:  f.sixHourReserved,
			FinalizedUsdMicros: f.sixHourFinal,
		},
	}, nil
}

func activeEntitlement() entitlement.Entitlement {
	return entitlement.Entitlement{
		SubscriberID: testSubscriber,
		Version:      7,
		Plan:         entitlement.PlanMax,
		Status:       entitlement.StatusActive,
		BillingPeriod: entitlement.Period{
			Kind:  entitlement.PeriodKindBilling,
			Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		},
		EffectiveAt:                      time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		MonthlyAllowanceUsdMicros:        maxMonthly,
		NominalMonthlyAllowanceUsdMicros: maxMonthly,
		SixHourAllowanceUsdMicros:        403_225,
		ProjectedAt:                      time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	}
}

func newService(entitlements *fakeEntitlements, allowances *fakeAllowances) *entitlement.Service {
	return entitlement.NewService(entitlements, allowances).WithClock(func() time.Time { return testNow })
}

func TestAdmitPassesThroughUnsubscribedCallers(t *testing.T) {
	t.Parallel()

	expired := activeEntitlement()
	expired.BillingPeriod.End = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	pastDue := activeEntitlement()
	pastDue.Status = entitlement.StatusPastDue

	for name, entitlements := range map[string]*fakeEntitlements{
		"no projected entitlement":       {},
		"entitlement no longer active":   {current: pastDue, found: true},
		"billing period already elapsed": {current: expired, found: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			allowances := &fakeAllowances{}
			admission, err := newService(entitlements, allowances).Admit(context.Background(), testSubscriber)
			require.NoError(t, err)
			assert.Equal(t, entitlement.AdmissionNotSubscribed, admission.Outcome)
			assert.Empty(t, admission.Coverage.SubscriberID, "an unsubscribed caller must not carry coverage")
		})
	}
}

func TestAdmitRejectsAnonymousCallerWithoutReadingStores(t *testing.T) {
	t.Parallel()

	entitlements := &fakeEntitlements{err: errors.New("must not be read")}
	admission, err := newService(entitlements, &fakeAllowances{}).Admit(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, entitlement.AdmissionNotSubscribed, admission.Outcome)
}

func TestAdmitCoversActiveSubscriberWithHeadroom(t *testing.T) {
	t.Parallel()

	allowances := &fakeAllowances{billingFinal: 1_000_000, sixHourFinal: 100_000}
	admission, err := newService(&fakeEntitlements{current: activeEntitlement(), found: true}, allowances).
		Admit(context.Background(), testSubscriber)
	require.NoError(t, err)

	assert.Equal(t, entitlement.AdmissionCovered, admission.Outcome)
	assert.Equal(t, testSubscriber, admission.Coverage.SubscriberID)
	assert.Equal(t, int64(7), admission.Coverage.EntitlementVersion)
	assert.Equal(t, entitlement.PlanMax, admission.Coverage.Plan)
	assert.Equal(t, maxMonthly, admission.Coverage.BillingLimitUsdMicros)
	assert.Equal(t, maxSixHour, admission.Coverage.SixHourLimitUsdMicros,
		"the window cap is derived from the nominal allowance, not from the projected average")
	assert.Equal(t, maxWeekly, admission.Coverage.WeeklyLimitUsdMicros)
	// The six-hour window is derived from the clock, not from the projection.
	assert.Equal(t, time.Date(2026, 3, 14, 6, 0, 0, 0, time.UTC), admission.Coverage.SixHourPeriod.Start)
	assert.Equal(t, time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC), admission.Coverage.SixHourPeriod.End)
	// Weeks run from the billing period start, not from a calendar weekday.
	assert.Equal(t, time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC), admission.Coverage.WeeklyPeriod.Start)
	assert.Equal(t, time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), admission.Coverage.WeeklyPeriod.End)
}

func TestAdmitBurstsAboveThePeriodAverageWindowShare(t *testing.T) {
	t.Parallel()

	admission, err := newService(&fakeEntitlements{current: activeEntitlement(), found: true}, &fakeAllowances{}).
		Admit(context.Background(), testSubscriber)
	require.NoError(t, err)

	assert.Equal(t, 4*int64(403_226), admission.Coverage.SixHourLimitUsdMicros,
		"a six-hour window carries four times its even share of the allowance")
	assert.Less(t, admission.Coverage.WeeklyLimitUsdMicros, 28*admission.Coverage.SixHourLimitUsdMicros,
		"the week must bind before a subscriber can burst every window of it")
}

func TestAdmitReportsExhaustedWindow(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		allowances *fakeAllowances
		expected   entitlement.PeriodKind
	}{
		"billing month spent": {
			allowances: &fakeAllowances{billingFinal: maxMonthly},
			expected:   entitlement.PeriodKindBilling,
		},
		"weekly window spent": {
			allowances: &fakeAllowances{weeklyFinal: maxWeekly},
			expected:   entitlement.PeriodKindWeekly,
		},
		"six-hour window spent": {
			allowances: &fakeAllowances{sixHourFinal: maxSixHour},
			expected:   entitlement.PeriodKindSixHour,
		},
		"held cost alone spends the window": {
			allowances: &fakeAllowances{sixHourReserved: maxSixHour},
			expected:   entitlement.PeriodKindSixHour,
		},
		"all spent reports the month": {
			allowances: &fakeAllowances{billingFinal: maxMonthly, weeklyFinal: maxWeekly, sixHourFinal: maxSixHour},
			expected:   entitlement.PeriodKindBilling,
		},
		"week and window spent reports the week": {
			allowances: &fakeAllowances{weeklyFinal: maxWeekly, sixHourFinal: maxSixHour},
			expected:   entitlement.PeriodKindWeekly,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			admission, err := newService(&fakeEntitlements{current: activeEntitlement(), found: true}, testCase.allowances).
				Admit(context.Background(), testSubscriber)
			require.NoError(t, err)
			assert.Equal(t, entitlement.AdmissionExhausted, admission.Outcome)
			assert.Equal(t, testCase.expected, admission.ExhaustedPeriod)
		})
	}
}

func TestAdmissionHeldCapacityOnly(t *testing.T) {
	for name, testCase := range map[string]struct {
		allowances *fakeAllowances
		wantHeld   bool
	}{
		"only outstanding holds reached cap": {allowances: &fakeAllowances{sixHourReserved: maxSixHour}, wantHeld: true},
		"mixed settled and held":             {allowances: &fakeAllowances{sixHourFinal: 100, sixHourReserved: maxSixHour - 100}, wantHeld: true},
		"already settled":                    {allowances: &fakeAllowances{sixHourFinal: maxSixHour}},
		"month settled while hour held":      {allowances: &fakeAllowances{billingFinal: maxMonthly, sixHourReserved: maxSixHour}},
	} {
		t.Run(name, func(t *testing.T) {
			admission, err := newService(&fakeEntitlements{current: activeEntitlement(), found: true}, testCase.allowances).
				Admit(context.Background(), testSubscriber)
			require.NoError(t, err)
			assert.Equal(t, testCase.wantHeld, admission.HeldCapacityOnly())
		})
	}
}

func TestAdmitDerivesTheWindowCapOfThePlanInForce(t *testing.T) {
	t.Parallel()

	upgraded := activeEntitlement()
	upgraded.Version = 8
	upgraded.Plan = entitlement.PlanBoost
	upgraded.NominalMonthlyAllowanceUsdMicros = 200_000_000
	// An upgrade mid-period only prorates the month; the window starts over at
	// the target plan's cap with the usage already booked in it still counted.
	upgraded.MonthlyAllowanceUsdMicros = 120_000_000
	spentUnderMax := &fakeAllowances{billingFinal: 10_000_000, sixHourFinal: 500_000}

	admission, err := newService(&fakeEntitlements{current: upgraded, found: true}, spentUnderMax).
		Admit(context.Background(), testSubscriber)
	require.NoError(t, err)

	assert.Equal(t, entitlement.AdmissionCovered, admission.Outcome)
	assert.Equal(t, 4*int64(1_612_903), admission.Coverage.SixHourLimitUsdMicros)
	assert.Equal(t, int64(40_000_000), admission.Coverage.WeeklyLimitUsdMicros)
	assert.Equal(t, int64(120_000_000), admission.Coverage.BillingLimitUsdMicros)
	assert.Equal(t, int64(500_000), admission.Usage.SixHour.ConsumedUsdMicros(),
		"a plan change must not forgive what the window already spent")
}

func TestAdmitSurfacesReadFailures(t *testing.T) {
	t.Parallel()

	readFailure := errors.New("database unreachable")

	_, err := newService(&fakeEntitlements{err: readFailure}, &fakeAllowances{}).Admit(context.Background(), testSubscriber)
	require.ErrorIs(t, err, readFailure, "an unreadable projection must not admit free usage")

	_, err = newService(&fakeEntitlements{current: activeEntitlement(), found: true}, &fakeAllowances{usageErr: readFailure}).
		Admit(context.Background(), testSubscriber)
	require.ErrorIs(t, err, readFailure, "unreadable usage must not admit free usage")
}

func servedSettlement() entitlement.Settlement {
	return entitlement.Settlement{
		Coverage: entitlement.Coverage{
			SubscriberID:       testSubscriber,
			AdmittedAt:         testNow,
			EntitlementVersion: 7,
			Plan:               entitlement.PlanMax,
			BillingPeriod: entitlement.Period{
				Kind:  entitlement.PeriodKindBilling,
				Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
				End:   time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			},
			WeeklyPeriod: entitlement.WeeklyWindowAt(entitlement.Period{
				Kind:  entitlement.PeriodKindBilling,
				Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
				End:   time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			}, testNow),
			SixHourPeriod:         entitlement.SixHourWindowAt(testNow),
			BillingLimitUsdMicros: maxMonthly,
			WeeklyLimitUsdMicros:  maxWeekly,
			SixHourLimitUsdMicros: maxSixHour,
		},
		ActionID:        "req-1:0",
		RouterRequestID: "req-1",
		APIKeyID:        testAPIKeyID,
		RequestedModel:  "claude-sonnet-4",
		ServedModel:     "claude-sonnet-4",
		RetailUsdMicros: 4_200,
		CapacitySource:  entitlement.CapacitySourceIncludedRouter,
	}
}

func TestSettleHoldsAndFinalizesActualCost(t *testing.T) {
	t.Parallel()

	allowances := &fakeAllowances{}
	require.NoError(t, newService(&fakeEntitlements{}, allowances).Settle(context.Background(), servedSettlement()))

	require.Len(t, allowances.reservations, 1)
	hold := allowances.reservations[0]
	assert.Equal(t, "req-1:0", hold.ActionID)
	assert.Equal(t, int64(4_200), hold.ReservedUsdMicros, "the hold carries the cost the turn actually incurred")
	assert.Equal(t, entitlement.CapacitySourceIncludedRouter, hold.CapacitySource)
	require.NoError(t, hold.Validate())

	require.Len(t, allowances.finalizes, 1)
	assert.Equal(t, int64(4_200), allowances.finalizes[0].RetailUsdMicros)
	assert.Equal(t, "claude-sonnet-4", allowances.finalizes[0].ServedModel)
}

func TestSettleFilesTheHoldInTheAdmittedWindow(t *testing.T) {
	t.Parallel()

	settlement := servedSettlement()
	allowances := &fakeAllowances{}
	crossedBoundary := func() time.Time { return settlement.Coverage.SixHourPeriod.End.Add(time.Minute) }

	require.NoError(t, newService(&fakeEntitlements{}, allowances).WithClock(crossedBoundary).
		Settle(context.Background(), settlement))

	require.Len(t, allowances.reservations, 1)
	hold := allowances.reservations[0]
	assert.Equal(t, settlement.Coverage.SixHourPeriod, hold.SixHourPeriod,
		"a turn that served past a boundary accrues where admission read it")
	require.NoError(t, hold.Validate())
}

func TestSettleIsIdempotentForRedeliveredAction(t *testing.T) {
	t.Parallel()

	allowances := &fakeAllowances{storedState: entitlement.ActionStateFinalized}
	require.NoError(t, newService(&fakeEntitlements{}, allowances).Settle(context.Background(), servedSettlement()))

	assert.Empty(t, allowances.finalizes, "an already settled action must not be charged twice")
}

func TestSettleSurfacesAccountingFailures(t *testing.T) {
	t.Parallel()

	accountingFailure := errors.New("accounting unavailable")

	err := newService(&fakeEntitlements{}, &fakeAllowances{reserveErr: accountingFailure}).
		Settle(context.Background(), servedSettlement())
	require.ErrorIs(t, err, accountingFailure)

	err = newService(&fakeEntitlements{}, &fakeAllowances{finalizeErr: accountingFailure}).
		Settle(context.Background(), servedSettlement())
	require.ErrorIs(t, err, entitlement.ErrAllowanceHeldUnsettled,
		"the hold already draws the windows down, so the caller must not book this turn elsewhere")
	assert.Contains(t, err.Error(), accountingFailure.Error())
}
