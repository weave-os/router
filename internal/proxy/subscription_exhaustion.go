package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/providers"
)

func (s *Service) claudeSubscriptionExhausted(ctx context.Context, headers http.Header) bool {
	return ownClaudeLoginRejected(ctx, headers, s.credentials.UsageObserver) && !credentials.PickedModelOnOwnPlan(ctx) || s.anthropicSubscriptionObservedExhausted(ctx, headers)
}

func (s *Service) anthropicSubscriptionObservedExhausted(ctx context.Context, headers http.Header) bool {
	if s.credentials.UsageObserver == nil {
		return false
	}
	_, anthroTok := credentials.PresentSubscriptionTokens(ctx, headers)
	return credentials.ClaudeSubscriptionObservedExhausted(s.credentials.UsageObserver, anthroTok, claudeSubscriptionFacts(ctx))
}

func claudeSnapshotUnservable(ctx context.Context, snap usage.Snapshot) bool {
	return credentials.ClaudeSnapshotUnservable(claudeSubscriptionFacts(ctx), snap)
}

func claudeSubscriptionFacts(ctx context.Context) credentials.ClaudeSubscriptionFacts {
	reason, subscriptionOnly := billing.SubscriptionOnlyReasonFromContext(ctx)
	return credentials.ClaudeSubscriptionFacts{
		EnterpriseOnly:       credentials.ClaudeEnterpriseOnly(ctx),
		PickedModelOnOwnPlan: credentials.PickedModelOnOwnPlan(ctx),
		OwnOverageBeforePool: credentials.OwnOverageBeforePool(ctx),
		EnterpriseSubscriptionOnly: subscriptionOnly && reason != billing.SubscriptionOnlyLinkedFirst &&
			credentials.ClaudeSubscriptionPlan(ctx) == providers.SubscriptionPlanEnterprise,
		NoPaidFallback: billing.WeaveCreditsDeniedFromContext(ctx) && credentials.UsableBYOK(ctx, providers.ProviderAnthropic) == nil &&
			!credentials.LinkedClaudeSeatConnected(ctx),
	}
}
