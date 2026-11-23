package credentials

import (
	"context"
	"net/http"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/subscriptions/entitlement"
)

type SubscriptionOnlyAttemptKey struct{}

type SubscriptionAPIOnlyKey struct{}

type SubscriptionRotationBudgetKey struct{}

func SubscriptionAttemptOnly(ctx context.Context) bool {
	value, _ := ctx.Value(SubscriptionOnlyAttemptKey{}).(bool)
	return value
}

func SubscriptionAPIOnly(ctx context.Context) bool {
	value, _ := ctx.Value(SubscriptionAPIOnlyKey{}).(bool)
	return value
}

func ManagedSubscriptionProviderFromUpstream(provider, model string) (subscriptions.Provider, bool) {
	pool, ok := subscriptions.PoolForUpstream(provider)
	if !ok || pool == subscriptions.ProviderCodex && !requestcontext.CodexSubscriptionCanAttemptModel(model) {
		return "", false
	}
	return pool, true
}

func ManagedSubscriptionEnrolled(ctx context.Context, provider subscriptions.Provider) bool {
	if provider == subscriptions.ProviderClaude && AnthropicBYOKFirst(ctx) ||
		provider == subscriptions.ProviderCodex && ByokFirst(ctx, providers.ProviderOpenAI) {
		return false
	}
	if SubscriptionAPIOnly(ctx) || SubscriptionRoutingDisabledForRequest(ctx) || SubscriptionFundingOutOfPlayForRequest(ctx) {
		return false
	}
	enrolled, _ := ctx.Value(ManagedSubscriptionProvidersContextKey{}).(map[auth.SubscriptionProvider]struct{})
	_, ok := enrolled[auth.SubscriptionProvider(provider)]
	return ok
}

func ManagedSubscriptionCanServe(ctx context.Context, provider, model string) bool {
	if provider == providers.ProviderOpenAI && CodexChatEndpoint(ctx) {
		return false
	}
	poolProvider, eligible := ManagedSubscriptionProviderFromUpstream(provider, model)
	return eligible && ManagedSubscriptionEnrolled(ctx, poolProvider)
}

func SubscriptionRoutingDisabledForRequest(ctx context.Context) bool {
	disabled, _ := ctx.Value(requestcontext.InstallationSubscriptionRoutingDisabledContextKey{}).(bool)
	return disabled
}

func SubscriptionFundingOutOfPlayForRequest(ctx context.Context) bool {
	plan, ok := entitlement.ProductScopeFromContext(ctx)
	return ok && plan == entitlement.PlanMax
}

func AnthropicSubscriptionFromContext(ctx context.Context) string {
	if AnthropicBYOKFirst(ctx) {
		return ""
	}
	v, _ := ctx.Value(AnthropicSubscriptionContextKey{}).(string)
	return v
}

type SuppressCodexSubscriptionContextKey struct{}

func WithSuppressedCodexSubscription(ctx context.Context) context.Context {
	return context.WithValue(ctx, SuppressCodexSubscriptionContextKey{}, true)
}

func CodexSubscriptionSuppressed(ctx context.Context) bool {
	v, _ := ctx.Value(SuppressCodexSubscriptionContextKey{}).(bool)
	return v
}

type SuppressCodexModelContextKey struct{}

func WithSuppressedCodexModel(ctx context.Context, model string) context.Context {
	previous, _ := ctx.Value(SuppressCodexModelContextKey{}).(map[string]struct{})
	models := make(map[string]struct{}, len(previous))
	for id := range previous {
		models[id] = struct{}{}
	}
	models[router.StripDateSuffix(model)] = struct{}{}
	return context.WithValue(ctx, SuppressCodexModelContextKey{}, models)
}

func CodexModelSuppressed(ctx context.Context, model string) bool {
	models, _ := ctx.Value(SuppressCodexModelContextKey{}).(map[string]struct{})
	_, suppressed := models[router.StripDateSuffix(model)]
	return suppressed
}

type CodexChatEndpointContextKey struct{}

func WithCodexChatEndpoint(ctx context.Context) context.Context {
	return context.WithValue(ctx, CodexChatEndpointContextKey{}, true)
}

func CodexChatEndpoint(ctx context.Context) bool {
	v, _ := ctx.Value(CodexChatEndpointContextKey{}).(bool)
	return v
}

func OpenAISubscriptionFromContext(ctx context.Context) string {
	if ByokFirst(ctx, providers.ProviderOpenAI) {
		return ""
	}
	v, _ := ctx.Value(OpenAISubscriptionContextKey{}).(string)
	return v
}

func CodexSubscriptionFromContext(ctx context.Context) *requestcontext.Credentials {
	return requestcontext.CodexSubscriptionCreds(OpenAISubscriptionFromContext(ctx), requestcontext.OpenAIAccountIDFromContext(ctx))
}

type SuppressClaudeModelContextKey struct{}

func ClaudeModelSuppressed(ctx context.Context, model string) bool {
	models, _ := ctx.Value(SuppressClaudeModelContextKey{}).(map[string]struct{})
	_, suppressed := models[router.StripDateSuffix(model)]
	return suppressed
}

func PresentSubscriptionTokens(ctx context.Context, headers http.Header) (codex, anthropic string) {
	if SubscriptionRoutingDisabledForRequest(ctx) || SubscriptionFundingOutOfPlayForRequest(ctx) {
		return "", ""
	}
	if CodexSubscriptionFromContext(ctx) != nil {
		codex = OpenAISubscriptionFromContext(ctx)
	} else if c := requestcontext.ExtractClientCredentials(providers.ProviderOpenAI, headers); c != nil && c.OAuth && !ByokFirst(ctx, providers.ProviderOpenAI) {
		codex = string(c.APIKey)
	}
	if !AnthropicBYOKFirst(ctx) {
		if creds := SubscriptionCredsFromToken(AnthropicSubscriptionFromContext(ctx)); creds != nil {
			anthropic = string(creds.APIKey)
		} else if c := requestcontext.ExtractClientCredentials(providers.ProviderAnthropic, headers); c != nil && c.OAuth {
			anthropic = string(c.APIKey)
		}
	}
	return codex, anthropic
}
