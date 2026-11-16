package entitlement

import "weave-os/router/internal/subscriptionlane"

var spentAllowancePeriods = map[subscriptionlane.Allowance]PeriodKind{
	subscriptionlane.AllowanceBillingSpent: PeriodKindBilling,
	subscriptionlane.AllowanceWeeklySpent:  PeriodKindWeekly,
	subscriptionlane.AllowanceSixHourSpent: PeriodKindSixHour,
}

func spentWindow(usage Usage) (bool, PeriodKind) {
	allowance := subscriptionlane.AllowanceUnspent
	for _, window := range [...]struct {
		usage WindowUsage
		on    subscriptionlane.AllowanceEvent
	}{
		{usage: usage.Billing, on: subscriptionlane.BillingWindowSpent},
		{usage: usage.Weekly, on: subscriptionlane.WeeklyWindowSpent},
		{usage: usage.SixHour, on: subscriptionlane.SixHourWindowSpent},
	} {
		if window.usage.ConsumedUsdMicros() >= window.usage.LimitUsdMicros {
			allowance = subscriptionlane.StepAllowance(allowance, window.on)
		}
	}
	kind, spent := spentAllowancePeriods[allowance]
	return spent, kind
}
