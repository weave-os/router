package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
)

func (s *Service) claudeSubscriptionExhausted(ctx context.Context, headers http.Header) bool {
	return ownClaudeLoginRejected(ctx, headers, s.credentials.UsageObserver) && !pickedModelOnOwnPlan(ctx) || s.anthropicSubscriptionObservedExhausted(ctx, headers)
}

func (s *Service) anthropicSubscriptionObservedExhausted(ctx context.Context, headers http.Header) bool {
	if s.credentials.UsageObserver == nil {
		return false
	}
	_, anthroTok := presentSubscriptionTokens(ctx, headers)
	return credentials.ClaudeSubscriptionObservedExhausted(s.credentials.UsageObserver, anthroTok, claudeSubscriptionFacts(ctx))
}

func claudeSnapshotUnservable(ctx context.Context, snap usage.Snapshot) bool {
	return credentials.ClaudeSnapshotUnservable(claudeSubscriptionFacts(ctx), snap)
}

func claudeSubscriptionFacts(ctx context.Context) credentials.ClaudeSubscriptionFacts {
	reason, subscriptionOnly := billing.SubscriptionOnlyReasonFromContext(ctx)
	return credentials.ClaudeSubscriptionFacts{
		EnterpriseOnly:       claudeEnterpriseOnly(ctx),
		PickedModelOnOwnPlan: pickedModelOnOwnPlan(ctx),
		OwnOverageBeforePool: ownOverageBeforePool(ctx),
		EnterpriseSubscriptionOnly: subscriptionOnly && reason != billing.SubscriptionOnlyLinkedFirst &&
			claudeSubscriptionPlan(ctx) == providers.SubscriptionPlanEnterprise,
		NoPaidFallback: billing.WeaveCreditsDeniedFromContext(ctx) && usableBYOK(ctx, providers.ProviderAnthropic) == nil &&
			!linkedClaudeSeatConnected(ctx),
	}
}
