package entitlement_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/subscriptions/entitlement"
)

func TestEntitlementValidateAcceptsProjectedMaxAndBoost(t *testing.T) {
	t.Parallel()

	for _, plan := range []entitlement.Plan{entitlement.PlanMax, entitlement.PlanBoost} {
		projected := validEntitlement(plan)
		require.NoError(t, projected.Validate())
	}
}

func TestEntitlementValidateRejectsUnknownVocabularyAndInvalidAmounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*entitlement.Entitlement)
	}{
		{name: "subscriber", mutate: func(value *entitlement.Entitlement) { value.SubscriberID = "" }},
		{name: "version", mutate: func(value *entitlement.Entitlement) { value.Version = 0 }},
		{name: "plan", mutate: func(value *entitlement.Entitlement) { value.Plan = "enterprise" }},
		{name: "status", mutate: func(value *entitlement.Entitlement) { value.Status = "trialing" }},
		{name: "period kind", mutate: func(value *entitlement.Entitlement) { value.BillingPeriod.Kind = entitlement.PeriodKindSixHour }},
		{name: "allowance", mutate: func(value *entitlement.Entitlement) { value.MonthlyAllowanceUsdMicros = -1 }},
		{name: "non utc", mutate: func(value *entitlement.Entitlement) {
			value.EffectiveAt = value.EffectiveAt.In(time.FixedZone("west", -3600))
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value := validEntitlement(entitlement.PlanMax)
			test.mutate(&value)
			require.ErrorIs(t, value.Validate(), entitlement.ErrInvalidContract)
		})
	}
}

func TestTimestampValidationRequiresCanonicalUTC(t *testing.T) {
	t.Parallel()

	london, err := time.LoadLocation("Europe/London")
	require.NoError(t, err)

	reservation := validReservation()
	finalizedAction := entitlement.Action{
		Reservation:     reservation,
		State:           entitlement.ActionStateFinalized,
		ServedModel:     "claude-sonnet-4-5",
		RetailUsdMicros: 98_000,
	}
	finalizedAt := reservation.ReservedAt.Add(time.Minute)
	finalizedAction.FinalizedAt = &finalizedAt

	releasedAction := entitlement.Action{
		Reservation: reservation,
		State:       entitlement.ActionStateReleased,
	}
	releasedAt := reservation.ReservedAt.Add(time.Minute)
	releasedAction.ReleasedAt = &releasedAt

	tests := []struct {
		name     string
		utcValue time.Time
		validate func(time.Time) error
	}{
		{
			name:     "period start",
			utcValue: reservation.SixHourPeriod.Start,
			validate: func(value time.Time) error {
				period := reservation.SixHourPeriod
				period.Start = value
				return period.Validate()
			},
		},
		{
			name:     "period end",
			utcValue: reservation.SixHourPeriod.End,
			validate: func(value time.Time) error {
				period := reservation.SixHourPeriod
				period.End = value
				return period.Validate()
			},
		},
		{
			name:     "entitlement effective at",
			utcValue: reservation.BillingPeriod.Start,
			validate: func(value time.Time) error {
				projected := validEntitlement(entitlement.PlanMax)
				projected.EffectiveAt = value
				return projected.Validate()
			},
		},
		{
			name:     "entitlement projected at",
			utcValue: reservation.BillingPeriod.Start.Add(time.Minute),
			validate: func(value time.Time) error {
				projected := validEntitlement(entitlement.PlanMax)
				projected.ProjectedAt = value
				return projected.Validate()
			},
		},
		{
			name:     "reservation reserved at",
			utcValue: reservation.ReservedAt,
			validate: func(value time.Time) error {
				valueReservation := reservation
				valueReservation.ReservedAt = value
				return valueReservation.Validate()
			},
		},
		{
			name:     "finalization finalized at",
			utcValue: finalizedAt,
			validate: func(value time.Time) error {
				finalization := entitlement.Finalization{
					ActionID:        reservation.ActionID,
					ServedModel:     finalizedAction.ServedModel,
					RetailUsdMicros: finalizedAction.RetailUsdMicros,
					CapacitySource:  entitlement.CapacitySourceLinkedClaude,
					FinalizedAt:     value,
				}
				return finalization.Validate()
			},
		},
		{
			name:     "release released at",
			utcValue: releasedAt,
			validate: func(value time.Time) error {
				return entitlement.Release{ActionID: reservation.ActionID, ReleasedAt: value}.Validate()
			},
		},
		{
			name:     "finalized action timestamp",
			utcValue: finalizedAt,
			validate: func(value time.Time) error {
				action := finalizedAction
				action.FinalizedAt = &value
				return action.Validate()
			},
		},
		{
			name:     "released action timestamp",
			utcValue: releasedAt,
			validate: func(value time.Time) error {
				action := releasedAction
				action.ReleasedAt = &value
				return action.Validate()
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			require.NoError(t, test.validate(test.utcValue))
			require.ErrorIs(t, test.validate(test.utcValue.In(london)), entitlement.ErrInvalidContract)
		})
	}
}

func TestReservationValidateRequiresEveryAccountingPeriod(t *testing.T) {
	t.Parallel()

	reservation := validReservation()

	require.NoError(t, reservation.Validate())
	reservation.SixHourPeriod.End = reservation.SixHourPeriod.End.Add(time.Second)
	require.ErrorIs(t, reservation.Validate(), entitlement.ErrInvalidContract)
	reservation.SixHourPeriod = entitlement.Period{
		Kind:  entitlement.PeriodKindSixHour,
		Start: reservation.BillingPeriod.Start.Add(time.Hour),
		End:   reservation.BillingPeriod.Start.Add(7 * time.Hour),
	}
	require.ErrorIs(t, reservation.Validate(), entitlement.ErrInvalidContract)

	offWeek := validReservation()
	offWeek.WeeklyPeriod.Start = offWeek.WeeklyPeriod.Start.Add(24 * time.Hour)
	require.ErrorIs(t, offWeek.Validate(), entitlement.ErrInvalidContract,
		"weeks run from the billing period start, not from the hold")
}

func TestReservationValidateRequiresWindowsContainingTheHold(t *testing.T) {
	t.Parallel()

	reservation := validReservation()
	require.NoError(t, reservation.Validate())

	outsideSixHour := reservation
	outsideSixHour.ReservedAt = reservation.SixHourPeriod.End
	require.ErrorIs(t, outsideSixHour.Validate(), entitlement.ErrInvalidContract)

	outsideWeek := reservation
	outsideWeek.ReservedAt = reservation.WeeklyPeriod.End
	outsideWeek.SixHourPeriod = entitlement.SixHourWindowAt(outsideWeek.ReservedAt)
	require.ErrorIs(t, outsideWeek.Validate(), entitlement.ErrInvalidContract)

	outsideBilling := reservation
	outsideBilling.BillingPeriod.End = reservation.ReservedAt.Add(-time.Hour)
	require.ErrorIs(t, outsideBilling.Validate(), entitlement.ErrInvalidContract)
}

func TestFinalizationAndReleaseValidateLifecycleInputs(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 18, 7, 0, 0, 0, time.UTC)
	finalization := entitlement.Finalization{
		ActionID:        "request_1_main",
		ServedModel:     "claude-sonnet-4-5",
		RetailUsdMicros: 98_000,
		CapacitySource:  entitlement.CapacitySourceLinkedClaude,
		FinalizedAt:     now,
	}
	require.NoError(t, finalization.Validate())
	finalization.CapacitySource = "organization"
	require.ErrorIs(t, finalization.Validate(), entitlement.ErrInvalidContract)

	release := entitlement.Release{ActionID: "request_2_main", ReleasedAt: now}
	require.NoError(t, release.Validate())
	release.ActionID = ""
	assert.ErrorIs(t, release.Validate(), entitlement.ErrInvalidContract)
}

func TestActionValidateRequiresStateSpecificOutcome(t *testing.T) {
	t.Parallel()

	reservation := validReservation()
	action := entitlement.Action{
		Reservation: reservation,
		State:       entitlement.ActionStateReserved,
	}
	require.NoError(t, action.Validate())

	action.State = entitlement.ActionStateFinalized
	action.ServedModel = "claude-sonnet-4-5"
	action.RetailUsdMicros = 98_000
	finalizedAt := reservation.ReservedAt.Add(time.Minute)
	action.FinalizedAt = &finalizedAt
	require.NoError(t, action.Validate())

	action.State = entitlement.ActionStateReleased
	require.ErrorIs(t, action.Validate(), entitlement.ErrInvalidContract)
}

func validReservation() entitlement.Reservation {
	start := time.Date(2026, 1, 15, 6, 0, 0, 0, time.UTC)
	return entitlement.Reservation{
		ActionID:           "request_1_main",
		RouterRequestID:    "request_1",
		SubscriberID:       "81cb9bc0-fb29-4ff5-9720-b8de95f63220",
		EntitlementVersion: 7,
		Plan:               entitlement.PlanBoost,
		BillingPeriod:      entitlement.Period{Kind: entitlement.PeriodKindBilling, Start: start, End: start.AddDate(0, 1, 0)},
		WeeklyPeriod:       entitlement.Period{Kind: entitlement.PeriodKindWeekly, Start: start, End: start.Add(7 * 24 * time.Hour)},
		SixHourPeriod:      entitlement.Period{Kind: entitlement.PeriodKindSixHour, Start: start, End: start.Add(6 * time.Hour)},
		APIKeyID:           "f8fc3d54-3652-46c5-be85-6727f5572e5a",
		RequestedModel:     "claude-opus-4-1",
		ReservedUsdMicros:  125_000,
		CapacitySource:     entitlement.CapacitySourceIncludedRouter,
		ReservedAt:         start.Add(time.Minute),
	}
}

func validEntitlement(plan entitlement.Plan) entitlement.Entitlement {
	start := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	nominal := int64(50_000_000)
	if plan == entitlement.PlanBoost {
		nominal = 200_000_000
	}
	return entitlement.Entitlement{
		SubscriberID:                     "81cb9bc0-fb29-4ff5-9720-b8de95f63220",
		Version:                          3,
		Plan:                             plan,
		Status:                           entitlement.StatusActive,
		BillingPeriod:                    entitlement.Period{Kind: entitlement.PeriodKindBilling, Start: start, End: start.AddDate(0, 1, 0)},
		EffectiveAt:                      start,
		MonthlyAllowanceUsdMicros:        nominal,
		NominalMonthlyAllowanceUsdMicros: nominal,
		SixHourAllowanceUsdMicros:        403_225,
		ProjectedAt:                      start.Add(time.Minute),
	}
}
