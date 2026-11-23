package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"

	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions"
)

func codexSubscriptionCoversModel(model string) bool {
	return requestcontext.CodexSubscriptionCoversModel(model)
}

func (s *Service) WithUsageObserver(obs *usage.Observer) *Service {
	s.credentials.UsageObserver = obs
	return s
}

func restrictToSubscriptionProviders(ctx context.Context, headers http.Header, enabled map[string]struct{}) map[string]struct{} {
	return restrictToProviders(enabled, credentials.SubscriptionServableProviders(ctx, headers))
}

func restrictToCustomerFundedProviders(ctx context.Context, headers http.Header, enabled map[string]struct{}) map[string]struct{} {
	if billing.ByokFeeUnpayable(ctx) {
		return restrictToSubscriptionProviders(ctx, headers, enabled)
	}
	return restrictToProviders(enabled, credentials.CustomerFundedProviders(ctx, headers))
}

func restrictToFundedProviders(ctx context.Context, headers http.Header, enabled map[string]struct{}) map[string]struct{} {
	if paidFallbackForbiddenByPlan(ctx) {
		return restrictToSubscriptionProviders(ctx, headers, enabled)
	}
	return restrictToCustomerFundedProviders(ctx, headers, enabled)
}

func restrictToProviders(enabled, allowed map[string]struct{}) map[string]struct{} {
	if len(allowed) == 0 {
		return enabled
	}
	out := make(map[string]struct{}, len(allowed))
	for p := range enabled {
		if _, ok := allowed[p]; ok {
			out[p] = struct{}{}
		}
	}
	return out
}

func RequestPresentsCoveringSubscription(ctx context.Context, headers http.Header, routePath string) bool {
	if credentials.SubscriptionFundingOutOfPlayForRequest(ctx) {
		return false
	}
	codex, anthropic := credentials.PresentSubscriptionTokens(ctx, headers)
	switch routePath {
	case requestcontext.RoutePathMessages, requestcontext.RoutePathPiHandoff:
		return anthropic != "" || credentials.ManagedSubscriptionEnrolled(ctx, subscriptions.ProviderClaude)
	case requestcontext.RoutePathChatCompletions, requestcontext.RoutePathResponses:
		return codex != "" || credentials.ManagedSubscriptionEnrolled(ctx, subscriptions.ProviderCodex)
	default:
		return false
	}
}

func RequestHasCustomerFunding(ctx context.Context, headers http.Header, routePath string, byokFeeOwed bool) bool {
	if RequestPresentsCoveringSubscription(ctx, headers, routePath) {
		return true
	}
	if byokFeeOwed {
		return false
	}
	for provider := range credentials.BuildCredentialsMap(credentials.ExternalKeysFromContext(ctx)) {
		if credentials.UsableBYOK(ctx, provider) != nil {
			return true
		}
	}
	return false
}

func (s *Service) withUsageObserver(ctx context.Context, headers http.Header) context.Context {
	ctx = s.withClaudeSubscriptionProfile(ctx, headers)
	codexTok, anthroTok := credentials.PresentSubscriptionTokens(ctx, headers)
	if s.credentials.UsageObserver == nil && anthroTok == "" && codexTok == "" && !credentials.ManagedSubscriptionEnrolled(ctx, subscriptions.ProviderCodex) && !credentials.ManagedSubscriptionEnrolled(ctx, subscriptions.ProviderClaude) {
		return ctx
	}
	ctx = credentials.WithUnifiedLimitCapture(ctx)
	obs := func(callCtx context.Context, h http.Header) {
		func() {
			defer func() {
				if r := recover(); r != nil {
					observability.FromContext(callCtx).Error("weave-capture Phase 0: recovered from panic capturing unified limit headers", "recover", r)
				}
			}()
			credentials.CaptureUnifiedLimitHeaders(callCtx, h)
		}()

		if s.credentials.UsageObserver == nil {
			return
		}
		creds := credentials.CredentialsFromContext(callCtx)
		if creds == nil || !creds.OAuth {
			return
		}
		switch creds.Source {
		case requestcontext.SourceCodexSubscription:
			if snap, ok := usage.ParseCodexHeaders(h); ok {
				s.credentials.UsageObserver.Record(s.subscriptionUsageKey(creds), snap)
				recordCallerUsage(callCtx, s.credentials.UsageObserver, creds, auth.SubscriptionProviderCodex, snap)
			}
		case requestcontext.SourceSubscription:
			if snap, ok := usage.ParseAnthropicUnifiedHeaders(h); ok {
				s.credentials.UsageObserver.Record(s.subscriptionUsageKey(creds), snap)
				recordCallerUsage(callCtx, s.credentials.UsageObserver, creds, auth.SubscriptionProviderClaude, snap)
			}
		}
	}
	return providers.WithUpstreamHeaderObserver(ctx, obs)
}

func (s *Service) subscriptionUsageKey(creds *requestcontext.Credentials) usage.CredentialKey {
	if creds.SubscriptionAccountID != "" {
		return s.credentials.UsageObserver.Key([]byte("subscription-account:" + creds.SubscriptionAccountID))
	}
	return s.credentials.UsageObserver.Key(creds.APIKey)
}

func (s *Service) selectableSubscriptionProviders(ctx context.Context, headers http.Header) map[string]struct{} {
	available := subscriptionProvidersForRequest(ctx)
	if credentials.SubscriptionRoutingDisabledForRequest(ctx) || credentials.SubscriptionFundingOutOfPlayForRequest(ctx) {
		return available
	}
	codexToken, claudeToken := credentials.PresentSubscriptionTokens(ctx, headers)
	if claudeToken != "" && !credentials.ClaudeSubscriptionSuppressed(ctx) && !claudeLoginSpent(ctx, s.credentials.UsageObserver, claudeToken) {
		available[providers.ProviderAnthropic] = struct{}{}
	}
	if codexToken != "" && !credentials.CodexSubscriptionSuppressed(ctx) && !codexLoginSpent(ctx, s.credentials.UsageObserver, codexToken, s.codexExtraCreditsAllowed()) {
		available[providers.ProviderOpenAI] = struct{}{}
	}
	for provider, creds := range credentials.BuildCredentialsMap(credentials.ExternalKeysFromContext(ctx)) {
		if len(creds.APIKey) > 0 && (provider == providers.ProviderAnthropic || provider == providers.ProviderOpenAI) {
			available[provider] = struct{}{}
		}
	}
	return available
}

func claudeLoginSpent(ctx context.Context, observer *usage.Observer, token string) bool {
	if observer == nil {
		return false
	}
	snap, ok := observer.Snapshot(observer.Key([]byte(token)))
	return ok && !snap.OverageInUse && snap.Exhausted() && claudeSnapshotUnservable(ctx, snap)
}

func codexLoginSpent(ctx context.Context, observer *usage.Observer, token string, extraCreditsAllowed bool) bool {
	if observer == nil || credentials.PickedModelOnOwnPlan(ctx) {
		return false
	}
	key := observer.Key([]byte(token))
	if observer.Rejected(key) {
		return true
	}
	snap, ok := observer.Snapshot(key)
	return ok && !snap.OverageInUse && snap.Exhausted() && !extraCreditsAllowed
}

func recordCallerUsage(ctx context.Context, observer *usage.Observer, creds *requestcontext.Credentials, provider auth.SubscriptionProvider, snap usage.Snapshot) {
	if creds.SubscriptionAccountID != "" {
		observer.Record(observer.Key(creds.APIKey), snap)
		return
	}
	observer.RecordSeat(ctx, auth.UserIDFrom(ctx), string(provider), snap)
}
