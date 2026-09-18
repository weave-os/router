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

func TestReservationValidateRequiresBothAccountingPeriods(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 9, 18, 6, 0, 0, 0, time.UTC)
	reservation := entitlement.Reservation{
		ActionID:           "request_1_main",
		RouterRequestID:    "request_1",
		SubscriberID:       "81cb9bc0-fb29-4ff5-9720-b8de95f63220",
		EntitlementVersion: 7,
		Plan:               entitlement.PlanBoost,
		BillingPeriod:      entitlement.Period{Kind: entitlement.PeriodKindBilling, Start: start, End: start.AddDate(0, 1, 0)},
		SixHourPeriod:      entitlement.Period{Kind: entitlement.PeriodKindSixHour, Start: start, End: start.Add(6 * time.Hour)},
		APIKeyID:           "f8fc3d54-3652-46c5-be85-6727f5572e5a",
		RequestedModel:     "claude-opus-4-1",
		ReservedUsdMicros:  125_000,
		CapacitySource:     entitlement.CapacitySourceIncludedRouter,
		ReservedAt:         start.Add(time.Minute),
	}

	require.NoError(t, reservation.Validate())
	reservation.SixHourPeriod.End = reservation.SixHourPeriod.End.Add(time.Second)
	require.ErrorIs(t, reservation.Validate(), entitlement.ErrInvalidContract)
	reservation.SixHourPeriod = entitlement.Period{
		Kind:  entitlement.PeriodKindSixHour,
		Start: start.Add(time.Hour),
		End:   start.Add(7 * time.Hour),
	}
	require.ErrorIs(t, reservation.Validate(), entitlement.ErrInvalidContract)
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

	start := time.Date(2026, 9, 18, 6, 0, 0, 0, time.UTC)
	action := entitlement.Action{
		Reservation: entitlement.Reservation{
			ActionID:           "request_1_main",
			RouterRequestID:    "request_1",
			SubscriberID:       "81cb9bc0-fb29-4ff5-9720-b8de95f63220",
			EntitlementVersion: 7,
			Plan:               entitlement.PlanBoost,
			BillingPeriod:      entitlement.Period{Kind: entitlement.PeriodKindBilling, Start: start, End: start.AddDate(0, 1, 0)},
			SixHourPeriod:      entitlement.Period{Kind: entitlement.PeriodKindSixHour, Start: start, End: start.Add(6 * time.Hour)},
			APIKeyID:           "f8fc3d54-3652-46c5-be85-6727f5572e5a",
			RequestedModel:     "claude-opus-4-1",
			ReservedUsdMicros:  125_000,
			CapacitySource:     entitlement.CapacitySourceIncludedRouter,
			ReservedAt:         start.Add(time.Minute),
		},
		State: entitlement.ActionStateReserved,
	}
	require.NoError(t, action.Validate())

	action.State = entitlement.ActionStateFinalized
	action.ServedModel = "claude-sonnet-4-5"
	action.RetailUsdMicros = 98_000
	finalizedAt := start.Add(2 * time.Minute)
	action.FinalizedAt = &finalizedAt
	require.NoError(t, action.Validate())

	action.State = entitlement.ActionStateReleased
	require.ErrorIs(t, action.Validate(), entitlement.ErrInvalidContract)
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
