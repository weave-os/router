package proxy

import (
	"context"
	"fmt"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/subscriptionlane"
	"weave-os/router/internal/subscriptions/entitlement"
)

// checkUserMonthlySpendLimit gates a turn on the resolved engineer's monthly
// spend limit. Runs inside the proxy (not middleware) because user identity is
// resolved by the handler after the middleware chain; no identity passes through.
func (s *Service) checkUserMonthlySpendLimit(ctx context.Context, headers http.Header, routePath string) (context.Context, error) {
	if s.billing == nil {
		return ctx, nil
	}
	// Billing override is the org-wide escape hatch (WithBalanceCheck stamps it
	// and passes those orgs through), so engineer limits don't apply either.
	if billing.HasOverrideFromContext(ctx) {
		return ctx, nil
	}
	// A turn covered by an individual Max/Boost allowance debits 0 and settles
	// against that allowance, so it adds nothing to the engineer's paid spend.
	if _, covered := entitlement.CoverageFromContext(ctx); covered {
		return ctx, nil
	}
	userID := auth.UserIDFrom(ctx)
	if userID == "" {
		return ctx, nil
	}
	orgID, _ := ctx.Value(ExternalIDContextKey{}).(string)
	if orgID == "" {
		return ctx, nil
	}
	result, err := s.billing.CheckUserMonthlySpend(ctx, orgID, userID)
	if err != nil {
		observability.FromContext(ctx).Error("User monthly spend-limit check failed; refusing request",
			"err", err, "organization_id", orgID, "router_user_id", userID)
		return ctx, fmt.Errorf("%w: %v", billing.ErrSpendLimitCheckUnavailable, err)
	}
	if result.LimitReached() {
		laneCtx, lane := s.billing.TripSubscriptionLane(ctx, subscriptionlane.UserSpendLimitGate.Tripped(
			RequestPresentsCoveringSubscription(ctx, headers, routePath)))
		if _, refused := lane.Refusal(); !refused {
			observability.FromContext(ctx).Info("Engineer monthly cap reached but subscription covers the route: serving subscription-only",
				"organization_id", orgID,
				"router_user_id", userID,
				"spent_usd_micros", result.SpentMicros,
				"monthly_limit_usd_micros", *result.LimitMicros,
			)
			return laneCtx, nil
		}
		observability.FromContext(ctx).Info("Request rejected: engineer monthly spend limit reached",
			"organization_id", orgID,
			"router_user_id", userID,
			"spent_usd_micros", result.SpentMicros,
			"monthly_limit_usd_micros", *result.LimitMicros,
		)
		return ctx, fmt.Errorf("%w: spent %d of %d usd micros", billing.ErrUserMonthlySpendLimitReached, result.SpentMicros, *result.LimitMicros)
	}
	return ctx, nil
}
