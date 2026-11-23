package proxy

import (
	"context"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

func withPickedModelFunding(ctx context.Context, servedAsIs bool, provider string) context.Context {
	if !auth.ClientModelPassthroughFrom(ctx) {
		return ctx
	}
	if !servedAsIs {
		return auth.WithoutClientModelPassthrough(ctx)
	}
	return fundOnOwnPlan(ctx, provider)
}

func withCodexPickFunding(ctx context.Context, servedModel, provider string) context.Context {
	selected, _, _ := strings.Cut(codexSelectedModel(ctx), ":")
	if selected == "" || selected != servedModel {
		return ctx
	}
	return fundOnOwnPlan(ctx, provider)
}

func fundOnOwnPlan(ctx context.Context, provider string) context.Context {
	if creditFallback, _ := ctx.Value(requestcontext.InstallationPickedModelCreditFallbackContextKey{}).(bool); creditFallback {
		return ctx
	}
	if provider == providers.ProviderAnthropic && credentials.AnthropicSubscriptionFromContext(ctx) == "" ||
		provider == providers.ProviderOpenAI && credentials.OpenAISubscriptionFromContext(ctx) == "" ||
		provider != providers.ProviderAnthropic && provider != providers.ProviderOpenAI {
		return ctx
	}
	if provider == providers.ProviderOpenAI {
		ctx = requestcontext.WithSubscriptionExtraUsage(ctx)
	}
	return context.WithValue(ctx, credentials.PickedModelOnOwnPlanContextKey{}, true)
}
