package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions"
)

const (
	routePathMessages        = "/v1/messages"
	routePathChatCompletions = "/v1/chat/completions"
	routePathResponses       = "/v1/responses"
)

func CodexSubscriptionCoversModel(model string) bool {
	return requestcontext.CodexSubscriptionCoversModel(model)
}

func codexSubscriptionCanAttemptModel(model string) bool {
	return requestcontext.CodexSubscriptionCanAttemptModel(model)
}

func codexSubscriptionCoversModel(model string) bool {
	return CodexSubscriptionCoversModel(model)
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
	sub := subscriptionServableProviders(ctx, headers)
	if len(sub) == 0 {
		return enabled
	}
	out := make(map[string]struct{}, len(sub))
	for p := range enabled {
		if _, ok := sub[p]; ok {
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
	case routePathMessages:
		return anthropic != "" || managedSubscriptionEnrolled(ctx, subscriptions.ProviderClaude)
	case routePathChatCompletions, routePathResponses:
		return codex != "" || managedSubscriptionEnrolled(ctx, subscriptions.ProviderCodex)
	default:
		return false
	}
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
				if creds.SubscriptionAccountID != "" {
					s.usageObserver.Record(s.usageObserver.Key(creds.APIKey), snap)
				}
			}
		case credSourceSubscription:
			if snap, ok := usage.ParseAnthropicUnifiedHeaders(h); ok {
				s.usageObserver.Record(s.subscriptionUsageKey(creds), snap)
				if creds.SubscriptionAccountID != "" {
					s.usageObserver.Record(s.usageObserver.Key(creds.APIKey), snap)
				}
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
