package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/subscriptions"
)

func codexLossyRescueNeeded(ctx context.Context, s *Service, headers http.Header) func(model string) bool {
	return func(model string) bool {
		return s.codexSubscriptionExhausted(ctx, headers) && !s.linkedSeatCanCover(ctx, providers.ProviderOpenAI, model) &&
			(codexSubscriptionCanAttemptModel(model) || !codexPoolHasFreeSeat(ctx, s))
	}
}

func codexPoolHasFreeSeat(ctx context.Context, s *Service) bool {
	if s.managedSubscriptions == nil || managedSubscriptionEnrollmentUnavailable(ctx) {
		return false
	}
	now := s.clock.Now()
	return managedSeatCanCover(ctx, subscriptions.ProviderCodex, now, func(candidate managedSubscriptionCandidate) bool {
		return s.managedAccountQuotaExhausted(ctx, candidate, now)
	})
}
