package proxy

import (
	"context"
	"errors"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/eligibility"
	"weave-os/router/internal/subscriptionlane"
)

type subscriptionEligibilityVerdict struct {
	refused  subscriptionlane.State
	sentinel error
}

var subscriptionEligibilityVerdicts = map[subscriptionlane.Event]subscriptionEligibilityVerdict{
	subscriptionlane.ClientAppRefused:           {refused: subscriptionlane.RefusedSubscriptionClientApp, sentinel: ErrSubscriptionClientAppRefused},
	subscriptionlane.ClaudeEnterpriseUnservable: {refused: subscriptionlane.RefusedClaudeEnterpriseOnly, sentinel: ErrClaudeEnterpriseSubscriptionUnavailable},
}

func subscriptionEligibilityRefusal(ctx context.Context, on subscriptionlane.Event) error {
	verdict := subscriptionEligibilityVerdicts[on]
	if next, _ := subscriptionlane.Step(billing.SubscriptionLaneFromContext(ctx), on); next == verdict.refused {
		return verdict.sentinel
	}
	return errors.Join(subscriptionOnlyUnavailable(ctx), verdict.sentinel)
}

func subscriptionClientAppRefusal(ctx context.Context) error {
	return subscriptionEligibilityRefusal(ctx, subscriptionlane.ClientAppRefused)
}

func claudeEnterpriseRefusal(ctx context.Context) error {
	return subscriptionEligibilityRefusal(ctx, subscriptionlane.ClaudeEnterpriseUnservable)
}

func productEligibilityRefusal(ctx context.Context, boundary eligibility.Boundary, model string) error {
	ineligible := catalog.CheckEligibility(boundary, model)
	if ineligible == nil {
		return nil
	}
	if next, _ := subscriptionlane.Step(billing.SubscriptionLaneFromContext(ctx), subscriptionlane.ModelOutsidePlan); next == subscriptionlane.RefusedProductIneligible {
		return ineligible
	}
	return errors.Join(subscriptionOnlyUnavailable(ctx), ineligible)
}
