package proxy

import (
	"context"
	"errors"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

// ErrClaudeEnterpriseSubscriptionUnavailable refuses a Claude turn rather
// than charging a different funding source when Enterprise-only is enabled.
var ErrClaudeEnterpriseSubscriptionUnavailable = errors.New("Claude Enterprise-only billing requires a usable Claude subscription")

type claudeEnterpriseTurnKey struct{}
type claudeSubscriptionTurnKey struct{}

// Enterprise billing applies only to a provider-verified Enterprise account.
// An organization flag cannot turn a Pro, Max, Team or unknown plan into one.
func claudeEnterpriseOnly(ctx context.Context) bool {
	return claudeEnterpriseEnabled(ctx) && claudeSubscriptionPlan(ctx) == providers.SubscriptionPlanEnterprise
}

func claudeEnterpriseEnabled(ctx context.Context) bool {
	if subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx) || anthropicBYOKFirst(ctx) {
		return false
	}
	if overrides, ok := flags.OverridesFromContext(ctx); ok {
		if enabled, configured := overrides.Bools[flags.KeyClaudeEnterpriseOnly]; configured {
			return enabled
		}
	}
	return true
}

func claudeEnterpriseTarget(ctx context.Context, target inference.Target) bool {
	if !claudeEnterpriseOnly(ctx) {
		return false
	}
	pool, compatible := subscriptionPoolForModel(target)
	return compatible && pool == subscriptions.ProviderClaude
}

func withClaudeEnterpriseTurn(ctx context.Context, decision router.Decision) context.Context {
	if pool, compatible := subscriptionPoolForModel(inference.Target{Provider: decision.Provider, CatalogID: decision.Model}); compatible && pool == subscriptions.ProviderClaude {
		ctx = context.WithValue(ctx, claudeSubscriptionTurnKey{}, true)
	}
	if !claudeEnterpriseTarget(ctx, inference.Target{Provider: decision.Provider, CatalogID: decision.Model}) {
		return ctx
	}
	return context.WithValue(ctx, claudeEnterpriseTurnKey{}, true)
}

func claudeEnterpriseTurn(ctx context.Context) bool {
	onEnterprise, _ := ctx.Value(claudeEnterpriseTurnKey{}).(bool)
	claudeTurn, _ := ctx.Value(claudeSubscriptionTurnKey{}).(bool)
	if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); claudeTurn && state != nil && state.ClaudeSubscriptionPlan == providers.SubscriptionPlanEnterprise {
		onEnterprise = true
	}
	return onEnterprise && claudeEnterpriseOnly(ctx)
}
