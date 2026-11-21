package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions"
)

const (
	routePathMessages        = "/v1/messages"
	routePathPiHandoff       = "/v1/route/handoff"
	routePathChatCompletions = "/v1/chat/completions"
	routePathResponses       = "/v1/responses"
)

func codexSubscriptionCanAttemptModel(model string) bool {
	return requestcontext.CodexSubscriptionCanAttemptModel(model)
}

func codexSubscriptionCoversModel(model string) bool {
	return requestcontext.CodexSubscriptionCoversModel(model)
}

func (s *Service) WithUsageObserver(obs *usage.Observer) *Service {
	s.usageObserver = obs
	return s
}

func presentSubscriptionTokens(ctx context.Context, headers http.Header) (codex, anthropic string) {
	if subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx) {
		return "", ""
	}
	if codexSubscriptionFromContext(ctx) != nil {
		codex = openaiSubscriptionFromContext(ctx)
	} else if c := ExtractClientCredentials(providers.ProviderOpenAI, headers); c != nil && c.OAuth && !byokFirst(ctx, providers.ProviderOpenAI) {
		codex = string(c.APIKey)
	}
	if !anthropicBYOKFirst(ctx) {
		if creds := subscriptionCredsFromToken(anthropicSubscriptionFromContext(ctx)); creds != nil {
			anthropic = string(creds.APIKey)
		} else if c := ExtractClientCredentials(providers.ProviderAnthropic, headers); c != nil && c.OAuth {
			anthropic = string(c.APIKey)
		}
	}
	return codex, anthropic
}

func subscriptionServableProviders(ctx context.Context, headers http.Header) map[string]struct{} {
	codex, anthropic := presentSubscriptionTokens(ctx, headers)
	out := make(map[string]struct{}, 2)
	if codex != "" || managedSubscriptionEnrolled(ctx, subscriptions.ProviderCodex) {
		out[providers.ProviderOpenAI] = struct{}{}
	}
	if anthropic != "" || managedSubscriptionEnrolled(ctx, subscriptions.ProviderClaude) {
		out[providers.ProviderAnthropic] = struct{}{}
	}
	return out
}

func restrictToSubscriptionProviders(ctx context.Context, headers http.Header, enabled map[string]struct{}) map[string]struct{} {
	return restrictToProviders(enabled, subscriptionServableProviders(ctx, headers))
}

func restrictToCustomerFundedProviders(ctx context.Context, headers http.Header, enabled map[string]struct{}) map[string]struct{} {
	if billing.ByokFeeUnpayable(ctx) {
		return restrictToSubscriptionProviders(ctx, headers, enabled)
	}
	return restrictToProviders(enabled, customerFundedProviders(ctx, headers))
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
	if subscriptionFundingOutOfPlayForRequest(ctx) {
		return false
	}
	codex, anthropic := presentSubscriptionTokens(ctx, headers)
	switch routePath {
	case routePathMessages, routePathPiHandoff:
		return anthropic != "" || managedSubscriptionEnrolled(ctx, subscriptions.ProviderClaude)
	case routePathChatCompletions, routePathResponses:
		return codex != "" || managedSubscriptionEnrolled(ctx, subscriptions.ProviderCodex)
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
		if usableBYOK(ctx, provider) != nil {
			return true
		}
	}
	return false
}

func (s *Service) withUsageObserver(ctx context.Context, headers http.Header) context.Context {
	ctx = s.withClaudeSubscriptionProfile(ctx, headers)
	codexTok, anthroTok := presentSubscriptionTokens(ctx, headers)
	if s.usageObserver == nil && anthroTok == "" && codexTok == "" && !managedSubscriptionEnrolled(ctx, subscriptions.ProviderCodex) && !managedSubscriptionEnrolled(ctx, subscriptions.ProviderClaude) {
		return ctx
	}
	ctx = withUnifiedLimitCapture(ctx)
	obs := func(callCtx context.Context, h http.Header) {
		func() {
			defer func() {
				if r := recover(); r != nil {
					observability.FromContext(callCtx).Error("weave-capture Phase 0: recovered from panic capturing unified limit headers", "recover", r)
				}
			}()
			captureUnifiedLimitHeaders(callCtx, h)
		}()

		if s.usageObserver == nil {
			return
		}
		creds := CredentialsFromContext(callCtx)
		if creds == nil || !creds.OAuth {
			return
		}
		switch creds.Source {
		case credSourceCodexSubscription:
			if snap, ok := usage.ParseCodexHeaders(h); ok {
				s.usageObserver.Record(s.subscriptionUsageKey(creds), snap)
				recordCallerUsage(callCtx, s.usageObserver, creds, auth.SubscriptionProviderCodex, snap)
			}
		case credSourceSubscription:
			if snap, ok := usage.ParseAnthropicUnifiedHeaders(h); ok {
				s.usageObserver.Record(s.subscriptionUsageKey(creds), snap)
				recordCallerUsage(callCtx, s.usageObserver, creds, auth.SubscriptionProviderClaude, snap)
			}
		}
	}
	return providers.WithUpstreamHeaderObserver(ctx, obs)
}

func (s *Service) subscriptionUsageKey(creds *Credentials) usage.CredentialKey {
	if creds.SubscriptionAccountID != "" {
		return s.usageObserver.Key([]byte("subscription-account:" + creds.SubscriptionAccountID))
	}
	return s.usageObserver.Key(creds.APIKey)
}

func (s *Service) selectableSubscriptionProviders(ctx context.Context, headers http.Header) map[string]struct{} {
	available := subscriptionProvidersForRequest(ctx)
	if subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx) {
		return available
	}
	codexToken, claudeToken := presentSubscriptionTokens(ctx, headers)
	if claudeToken != "" && !claudeSubscriptionSuppressed(ctx) && !claudeLoginSpent(ctx, s.usageObserver, claudeToken) {
		available[providers.ProviderAnthropic] = struct{}{}
	}
	if codexToken != "" && !codexSubscriptionSuppressed(ctx) && !codexLoginSpent(ctx, s.usageObserver, codexToken, s.codexExtraCreditsAllowed()) {
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
	if observer == nil || pickedModelOnOwnPlan(ctx) {
		return false
	}
	key := observer.Key([]byte(token))
	if observer.Rejected(key) {
		return true
	}
	snap, ok := observer.Snapshot(key)
	return ok && !snap.OverageInUse && snap.Exhausted() && !extraCreditsAllowed
}

func recordCallerUsage(ctx context.Context, observer *usage.Observer, creds *Credentials, provider auth.SubscriptionProvider, snap usage.Snapshot) {
	if creds.SubscriptionAccountID != "" {
		observer.Record(observer.Key(creds.APIKey), snap)
		return
	}
	observer.RecordSeat(ctx, auth.UserIDFrom(ctx), string(provider), snap)
}
