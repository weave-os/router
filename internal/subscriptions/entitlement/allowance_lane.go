package entitlement

import "weave-os/router/internal/subscriptionlane"

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

func spentWindow(usage Usage) (bool, PeriodKind) {
	allowance := subscriptionlane.AllowanceUnspent
	for _, window := range windowSpentEvents {
		if spent := window.usage(usage); spent.ConsumedUsdMicros() >= spent.LimitUsdMicros {
			allowance, _ = subscriptionlane.StepAllowance(allowance, window.on)
		}
	}
	kind, spent := spentAllowancePeriods[allowance]
	return spent, kind
}
