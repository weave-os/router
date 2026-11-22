package proxy

import (
	"context"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/subscriptionlane"
)

func paidCapacityUnfunded(ctx context.Context) bool {
	return !subscriptionlane.Permits(billing.SubscriptionLaneFromContext(ctx), subscriptionlane.OrganizationCreditsServed)
}
