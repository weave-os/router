package credentials

import (
	"context"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

func FundOnOwnPlan(ctx context.Context, provider string) context.Context {
	if creditFallback, _ := ctx.Value(requestcontext.InstallationPickedModelCreditFallbackContextKey{}).(bool); creditFallback {
		return ctx
	}
	if provider == providers.ProviderAnthropic && AnthropicSubscriptionFromContext(ctx) == "" ||
		provider == providers.ProviderOpenAI && OpenAISubscriptionFromContext(ctx) == "" ||
		provider != providers.ProviderAnthropic && provider != providers.ProviderOpenAI {
		return ctx
	}
	if provider == providers.ProviderOpenAI {
		ctx = requestcontext.WithSubscriptionExtraUsage(ctx)
	}
	return context.WithValue(ctx, PickedModelOnOwnPlanContextKey{}, true)
}
