package entitlement_test

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdmitNamesTheLongestSpentWindowForEverySpentCombination(t *testing.T) {
	t.Parallel()

	resets := map[entitlement.PeriodKind]time.Time{
		entitlement.PeriodKindBilling: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		entitlement.PeriodKindWeekly:  time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
		entitlement.PeriodKindSixHour: time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC),
	}
	for mask := range 8 {
		billing, weekly, sixHour := mask&4 != 0, mask&2 != 0, mask&1 != 0
		allowances := &fakeAllowances{}
		if billing {
			allowances.billingFinal = maxMonthly
		}
		if weekly {
			allowances.weeklyFinal = maxWeekly
		}
		if sixHour {
			allowances.sixHourFinal = maxSixHour
		}
		expected := entitlement.PeriodKind("")
		switch {
		case billing:
			expected = entitlement.PeriodKindBilling
		case weekly:
			expected = entitlement.PeriodKindWeekly
		case sixHour:
			expected = entitlement.PeriodKindSixHour
		}

		admission, err := newService(&fakeEntitlements{current: activeEntitlement(), found: true}, allowances).
			Admit(context.Background(), testSubscriber)
		require.NoError(t, err)

		if expected == "" {
			assert.Equal(t, entitlement.AdmissionCovered, admission.Outcome, "mask %03b", mask)
			assert.Empty(t, admission.ExhaustedPeriod, "mask %03b", mask)
			continue
		}
		assert.Equal(t, entitlement.AdmissionExhausted, admission.Outcome, "mask %03b", mask)
		assert.Equal(t, expected, admission.ExhaustedPeriod, "mask %03b", mask)
		assert.Equal(t, resets[expected], admission.ResetsAt, "mask %03b", mask)
		assert.Empty(t, admission.Coverage.SubscriberID, "mask %03b: an exhausted admission carries no coverage", mask)
	}
}
