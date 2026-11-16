package providers

import "context"

type SubscriptionPlan string

const (
	SubscriptionPlanUnknown    SubscriptionPlan = "unknown"
	SubscriptionPlanPro        SubscriptionPlan = "pro"
	SubscriptionPlanMax        SubscriptionPlan = "max"
	SubscriptionPlanTeam       SubscriptionPlan = "team"
	SubscriptionPlanEnterprise SubscriptionPlan = "enterprise"
)

type SubscriptionPlanReader interface {
	SubscriptionPlan(context.Context, []byte) (SubscriptionPlan, error)
}
