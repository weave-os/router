package entitlement

import (
	"context"

	"weave-os/router/internal/subscriptionlane"
)

var windowSpentEvents = []struct {
	usage func(Usage) WindowUsage
	on    subscriptionlane.AllowanceEvent
}{
	{usage: func(u Usage) WindowUsage { return u.Billing }, on: subscriptionlane.BillingWindowSpent},
	{usage: func(u Usage) WindowUsage { return u.Weekly }, on: subscriptionlane.WeeklyWindowSpent},
	{usage: func(u Usage) WindowUsage { return u.SixHour }, on: subscriptionlane.SixHourWindowSpent},
}

var spentAllowancePeriods = map[subscriptionlane.Allowance]PeriodKind{
	subscriptionlane.AllowanceBillingSpent: PeriodKindBilling,
	subscriptionlane.AllowanceWeeklySpent:  PeriodKindWeekly,
	subscriptionlane.AllowanceSixHourSpent: PeriodKindSixHour,
}

var periodSpentAllowances = map[PeriodKind]subscriptionlane.Allowance{
	PeriodKindBilling: subscriptionlane.AllowanceBillingSpent,
	PeriodKindWeekly:  subscriptionlane.AllowanceWeeklySpent,
	PeriodKindSixHour: subscriptionlane.AllowanceSixHourSpent,
}

func spentWindow(usage Usage) (bool, PeriodKind) {
	allowance := subscriptionlane.AllowanceNotSubscribed
	for _, window := range windowSpentEvents {
		if spent := window.usage(usage); spent.ConsumedUsdMicros() >= spent.LimitUsdMicros {
			allowance, _ = subscriptionlane.StepAllowance(allowance, window.on)
		}
	}
	kind, spent := spentAllowancePeriods[allowance]
	return spent, kind
}

func AllowanceFromContext(ctx context.Context) subscriptionlane.Allowance {
	if _, covered := CoverageFromContext(ctx); covered {
		return subscriptionlane.AllowanceCovered
	}
	if window, spent := SpentWindowFromContext(ctx); spent {
		if allowance, known := periodSpentAllowances[window.Kind]; known {
			return allowance
		}
	}
	if _, scoped := ProductScopeFromContext(ctx); scoped {
		return subscriptionlane.AllowanceNotSubscribed
	}
	return subscriptionlane.AllowanceUnscoped
}
