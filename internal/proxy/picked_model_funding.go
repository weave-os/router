package proxy

import (
	"context"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

type pickedModelOnOwnPlanContextKey struct{}

// withPickedModelFunding funds a client-picked model the way the native client
// would: on the caller's own Claude/ChatGPT login, then that provider's own
// extra usage, never Weave credits, unless the installation opted into a credit
// fallback. Without a login for the served provider the pick funds like routed
// traffic. A pick the installation could not serve as-is was routed instead,
// so it stops counting as picked.
func withPickedModelFunding(ctx context.Context, servedAsIs bool, provider string) context.Context {
	if !auth.ClientModelPassthroughFrom(ctx) {
		return ctx
	}
	if !servedAsIs {
		return auth.WithoutClientModelPassthrough(ctx)
	}
	return fundOnOwnPlan(ctx, provider)
}

// withCodexPickFunding funds a Codex /model pick served as selected the same
// way as a client-picked model.
func withCodexPickFunding(ctx context.Context, servedModel, provider string) context.Context {
	selected, _, _ := strings.Cut(codexSelectedModel(ctx), ":")
	if selected == "" || selected != servedModel {
		return ctx
	}
	return fundOnOwnPlan(ctx, provider)
}

func fundOnOwnPlan(ctx context.Context, provider string) context.Context {
	if creditFallback, _ := ctx.Value(InstallationPickedModelCreditFallbackContextKey{}).(bool); creditFallback {
		return ctx
	}
	if provider == providers.ProviderAnthropic && anthropicSubscriptionFromContext(ctx) == "" ||
		provider == providers.ProviderOpenAI && openaiSubscriptionFromContext(ctx) == "" ||
		provider != providers.ProviderAnthropic && provider != providers.ProviderOpenAI {
		return ctx
	}
	if provider == providers.ProviderOpenAI {
		ctx = requestcontext.WithSubscriptionExtraUsage(ctx)
	}
	return context.WithValue(ctx, pickedModelOnOwnPlanContextKey{}, true)
}

// pickedModelOnOwnPlan reports a picked model bound to the caller's own login:
// observed exhaustion or overage does not move it off that login, and no
// metered capacity rescues it.
func pickedModelOnOwnPlan(ctx context.Context) bool {
	onOwnPlan, _ := ctx.Value(pickedModelOnOwnPlanContextKey{}).(bool)
	return onOwnPlan
}
