package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions/entitlement"
)

func (s *Service) checkUserMonthlySpendLimit(ctx context.Context, headers http.Header, routePath string) (context.Context, error) {
	if s.billing == nil {
		return ctx, nil
	}
	if billing.HasOverrideFromContext(ctx) {
		return ctx, nil
	}
	if _, covered := entitlement.CoverageFromContext(ctx); covered {
		return ctx, nil
	}
	userID := auth.UserIDFrom(ctx)
	if userID == "" {
		return ctx, nil
	}
	orgID, _ := ctx.Value(requestcontext.ExternalIDContextKey{}).(string)
	if orgID == "" {
		return ctx, nil
	}
	return billing.CheckUserMonthlySpendLimit(ctx, s.billing, orgID, userID, func() bool {
		return credentials.RequestPresentsCoveringSubscription(ctx, headers, routePath)
	})
}
