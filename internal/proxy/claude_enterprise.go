package proxy

import (
	"context"
	"fmt"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

// ErrClaudeEnterpriseSubscriptionUnavailable refuses a Claude turn rather
// than charging a different funding source when Enterprise-only is enabled.
var ErrClaudeEnterpriseSubscriptionUnavailable = fmt.Errorf("%w: Claude Enterprise-only billing requires a usable Claude subscription", ErrSubscriptionPoolUnavailable)

type claudeEnterpriseTurnKey struct{}
type claudeSubscriptionPresentKey struct{}

// Claude account funding defaults on for linked accounts. Explicit BYOK and
// credits-only configuration retain their existing authority over the default.
func claudeEnterpriseOnly(ctx context.Context) bool {
	if subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx) {
		return false
	}
	if overrides, ok := flags.OverridesFromContext(ctx); ok {
		if enabled, configured := overrides.Bools[flags.KeyClaudeEnterpriseOnly]; configured {
			return enabled
		}
	}
	present, _ := ctx.Value(claudeSubscriptionPresentKey{}).(bool)
	enrolled, _ := ctx.Value(ManagedSubscriptionProvidersContextKey{}).(map[auth.SubscriptionProvider]struct{})
	_, linked := enrolled[auth.SubscriptionProviderClaude]
	if !present && !linked && anthropicSubscriptionFromContext(ctx) == "" {
		return false
	}
	return !anthropicBYOKConfigured(ctx)
}

func claudeEnterpriseTarget(ctx context.Context, target inference.Target) bool {
	if !claudeEnterpriseOnly(ctx) {
		return false
	}
	pool, compatible := subscriptionPoolForModel(target)
	return compatible && pool == subscriptions.ProviderClaude
}

func withClaudeEnterpriseTurn(ctx context.Context, decision router.Decision) context.Context {
	if !claudeEnterpriseTarget(ctx, inference.Target{Provider: decision.Provider, CatalogID: decision.Model}) {
		return ctx
	}
	return context.WithValue(ctx, claudeEnterpriseTurnKey{}, true)
}

func claudeEnterpriseTurn(ctx context.Context) bool {
	onEnterprise, _ := ctx.Value(claudeEnterpriseTurnKey{}).(bool)
	return onEnterprise
}
