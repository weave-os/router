package credentials

import (
	"context"
	"errors"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

var ErrClaudeEnterpriseSubscriptionUnavailable = errors.New("enterprise-only Claude billing requires a usable Claude subscription")

type ClaudeEnterpriseTurnKey struct{}

type ClaudeSubscriptionTurnKey struct{}

func ClaudeEnterpriseOnly(ctx context.Context) bool {
	return ClaudeEnterpriseEnabled(ctx) && ClaudeSubscriptionPlan(ctx) == providers.SubscriptionPlanEnterprise
}

func ClaudeEnterpriseEnabled(ctx context.Context) bool {
	if SubscriptionRoutingDisabledForRequest(ctx) || SubscriptionFundingOutOfPlayForRequest(ctx) || AnthropicBYOKFirst(ctx) {
		return false
	}
	if overrides, ok := flags.OverridesFromContext(ctx); ok {
		if enabled, configured := overrides.Bools[flags.KeyClaudeEnterpriseOnly]; configured {
			return enabled
		}
	}
	return true
}

func ClaudeEnterpriseTarget(ctx context.Context, target inference.Target) bool {
	if !ClaudeEnterpriseOnly(ctx) {
		return false
	}
	pool, compatible := SubscriptionPoolForModel(target)
	return compatible && pool == subscriptions.ProviderClaude
}

func WithClaudeEnterpriseTurn(ctx context.Context, decision router.Decision) context.Context {
	if pool, compatible := SubscriptionPoolForModel(inference.Target{Provider: decision.Provider, CatalogID: decision.Model}); compatible && pool == subscriptions.ProviderClaude {
		ctx = context.WithValue(ctx, ClaudeSubscriptionTurnKey{}, true)
	}
	if !ClaudeEnterpriseTarget(ctx, inference.Target{Provider: decision.Provider, CatalogID: decision.Model}) {
		return ctx
	}
	return context.WithValue(ctx, ClaudeEnterpriseTurnKey{}, true)
}

func ClaudeEnterpriseTurn(ctx context.Context) bool {
	onEnterprise, _ := ctx.Value(ClaudeEnterpriseTurnKey{}).(bool)
	claudeTurn, _ := ctx.Value(ClaudeSubscriptionTurnKey{}).(bool)
	if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); claudeTurn && state != nil && state.ClaudeSubscriptionPlan == providers.SubscriptionPlanEnterprise {
		onEnterprise = true
	}
	return onEnterprise && ClaudeEnterpriseOnly(ctx)
}

func ClaudeEnterpriseCapRescue(ctx context.Context) bool {
	state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	return state != nil && state.ClaudeEnterpriseCapRefused && UsableBYOK(ctx, providers.ProviderAnthropic) != nil
}

func SubscriptionPoolForModel(target inference.Target) (subscriptions.Provider, bool) {
	if entry, known := catalog.ByID(target.CatalogID); known {
		for _, binding := range entry.Providers {
			if pool, compatible := ManagedSubscriptionProviderFromUpstream(binding.Provider, target.CatalogID); compatible {
				return pool, true
			}
		}
	}
	return ManagedSubscriptionProviderFromUpstream(target.Provider, target.CatalogID)
}

type ClaudeSubscriptionPlanKey struct{}

func ClaudeSubscriptionPlan(ctx context.Context) providers.SubscriptionPlan {
	if creds := CredentialsFromContext(ctx); creds != nil && creds.OAuth && creds.Source == requestcontext.SourceSubscription && creds.SubscriptionPlan != "" {
		return creds.SubscriptionPlan
	}
	if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); state != nil && state.ClaudeSubscriptionPlan != "" {
		return state.ClaudeSubscriptionPlan
	}
	plan, _ := ctx.Value(ClaudeSubscriptionPlanKey{}).(providers.SubscriptionPlan)
	return plan
}
