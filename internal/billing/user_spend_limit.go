package billing

import (
	"context"
	"fmt"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/subscriptionlane"
)

func CheckUserMonthlySpendLimit(ctx context.Context, svc *Service, orgID, userID string, covered func() bool) (context.Context, error) {
	spend, err := svc.CheckUserMonthlySpend(ctx, orgID, userID)
	if err != nil {
		observability.FromContext(ctx).Error("User monthly spend-limit check failed; refusing request",
			"err", err, "organization_id", orgID, "router_user_id", userID)
		return ctx, fmt.Errorf("%w: %v", ErrSpendLimitCheckUnavailable, err)
	}
	if spend.LimitReached() {
		laneCtx, lane := svc.TripSubscriptionLane(ctx, subscriptionlane.UserSpendLimitGate.Tripped(covered()))
		if _, refused := lane.Refusal(); !refused {
			observability.FromContext(ctx).Info("Engineer monthly cap reached but subscription covers the route: serving subscription-only",
				"organization_id", orgID,
				"router_user_id", userID,
				"spent_usd_micros", spend.SpentMicros,
				"monthly_limit_usd_micros", *spend.EffectiveLimitMicros(),
				"granted_usd_micros", spend.GrantedMicros,
			)
			return laneCtx, nil
		}
		observability.FromContext(ctx).Info("Request rejected: engineer monthly spend limit reached",
			"organization_id", orgID,
			"router_user_id", userID,
			"spent_usd_micros", spend.SpentMicros,
			"monthly_limit_usd_micros", *spend.EffectiveLimitMicros(),
			"granted_usd_micros", spend.GrantedMicros,
		)
		return ctx, fmt.Errorf("%w: spent %d of %d usd micros", ErrUserMonthlySpendLimitReached, spend.SpentMicros, *spend.EffectiveLimitMicros())
	}
	return ctx, nil
}
