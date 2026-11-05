package proxy

import (
	"context"
	"fmt"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

// ErrClaudeEnterpriseSubscriptionUnavailable refuses a Claude turn rather
// than charging a different funding source when Enterprise-only is enabled.
var ErrClaudeEnterpriseSubscriptionUnavailable = fmt.Errorf("%w: Claude Enterprise-only billing requires a usable Claude subscription", ErrSubscriptionPoolUnavailable)

type claudeEnterpriseTurnKey struct{}

// claudeEnterpriseOnly is an explicit organization preference. The existing
// credits-only mode takes precedence when subscriptions are disabled.
func claudeEnterpriseOnly(ctx context.Context) bool {
	return !subscriptionRoutingDisabledForRequest(ctx) && !subscriptionFundingOutOfPlayForRequest(ctx) &&
		flags.BoolOr(ctx, flags.KeyClaudeEnterpriseOnly, false)
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
