package entitlement_test

import (
	"context"
	"testing"

	"weave-os/router/internal/subscriptionlane"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/stretchr/testify/assert"
)

func TestAllowanceFromContextReadsTheAdmissionStamps(t *testing.T) {
	t.Parallel()

	scoped := entitlement.WithProductScope(context.Background(), entitlement.PlanMax)
	spent := func(kind entitlement.PeriodKind) context.Context {
		return entitlement.WithSpentWindow(scoped, entitlement.SpentWindow{Plan: entitlement.PlanMax, Kind: kind})
	}
	for name, testCase := range map[string]struct {
		ctx      context.Context
		expected subscriptionlane.Allowance
	}{
		"no subscriber":        {ctx: context.Background(), expected: subscriptionlane.AllowanceUnscoped},
		"scoped, not admitted": {ctx: scoped, expected: subscriptionlane.AllowanceNotSubscribed},
		"covered":              {ctx: entitlement.WithCoverage(scoped, entitlement.Coverage{SubscriberID: testSubscriber}), expected: subscriptionlane.AllowanceCovered},
		"billing spent":        {ctx: spent(entitlement.PeriodKindBilling), expected: subscriptionlane.AllowanceBillingSpent},
		"weekly spent":         {ctx: spent(entitlement.PeriodKindWeekly), expected: subscriptionlane.AllowanceWeeklySpent},
		"six-hour spent":       {ctx: spent(entitlement.PeriodKindSixHour), expected: subscriptionlane.AllowanceSixHourSpent},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, testCase.expected, entitlement.AllowanceFromContext(testCase.ctx))
		})
	}
}
