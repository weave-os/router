package providers

import "context"

// SubscriptionPlan is an account's plan verified by its subscription provider.
type SubscriptionPlan string

const (
	SubscriptionPlanUnknown    SubscriptionPlan = "unknown"
	SubscriptionPlanPro        SubscriptionPlan = "pro"
	SubscriptionPlanMax        SubscriptionPlan = "max"
	SubscriptionPlanTeam       SubscriptionPlan = "team"
	SubscriptionPlanEnterprise SubscriptionPlan = "enterprise"
)

// SubscriptionPlanReader reads account identity without performing inference.
type SubscriptionPlanReader interface {
	SubscriptionPlan(context.Context, []byte) (SubscriptionPlan, error)
}
