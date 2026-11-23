package credentials

import (
	"context"
	"errors"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/eligibility"
	"weave-os/router/internal/subscriptionlane"
)

func laneRefusal(ctx context.Context, on subscriptionlane.Event, refused subscriptionlane.State, sentinel error) error {
	if next, _ := subscriptionlane.Step(billing.SubscriptionLaneFromContext(ctx), on); next == refused {
		return sentinel
	}
	return errors.Join(SubscriptionOnlyUnavailable(ctx), sentinel)
}

func SubscriptionClientAppRefusal(ctx context.Context) error {
	return laneRefusal(ctx, subscriptionlane.ClientAppRefused, subscriptionlane.RefusedSubscriptionClientApp, ErrSubscriptionClientAppRefused)
}

func ClaudeEnterpriseRefusal(ctx context.Context) error {
	return laneRefusal(ctx, subscriptionlane.ClaudeEnterpriseUnservable, subscriptionlane.RefusedClaudeEnterpriseOnly, ErrClaudeEnterpriseSubscriptionUnavailable)
}

func ProductEligibilityRefusal(ctx context.Context, boundary eligibility.Boundary, model string) error {
	ineligible := catalog.CheckEligibility(boundary, model)
	if ineligible == nil {
		return nil
	}
	return laneRefusal(ctx, subscriptionlane.ModelOutsidePlan, subscriptionlane.RefusedProductIneligible, ineligible)
}
