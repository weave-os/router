package proxy

import (
	"context"
	"errors"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

// ErrClaudeEnterpriseSubscriptionUnavailable refuses a Claude turn rather
// than charging a different funding source when Enterprise-only is enabled.
var ErrClaudeEnterpriseSubscriptionUnavailable = errors.New("Claude Enterprise-only billing requires a usable Claude subscription")

type claudeEnterpriseTurnKey struct{}
type claudeSubscriptionPresentKey struct{}

// Claude account funding defaults on for linked accounts. Explicit BYOK and
// credits-only configuration retain their existing authority over the default,
// and the default yields once a plan seat proves it cannot serve the request.
func claudeEnterpriseOnly(ctx context.Context) bool {
	enabled, explicit := claudeEnterpriseSetting(ctx)
	if !enabled || explicit {
		return enabled
	}
	state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	return state == nil || !state.ClaudePlanQuotaRefused
}

// claudeSeatOverageAllowed keeps linked seats on provider-authorized overage
// even after a sibling plan seat released the request's funding default.
func claudeSeatOverageAllowed(ctx context.Context) bool {
	enabled, _ := claudeEnterpriseSetting(ctx)
	return enabled
}

func claudeEnterpriseSetting(ctx context.Context) (enabled, explicit bool) {
	if subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx) || anthropicBYOKConfigured(ctx) {
		return false, false
	}
	if overrides, ok := flags.OverridesFromContext(ctx); ok {
		if enabled, configured := overrides.Bools[flags.KeyClaudeEnterpriseOnly]; configured {
			return enabled, true
		}
	}
	present, _ := ctx.Value(claudeSubscriptionPresentKey{}).(bool)
	enrolled, _ := ctx.Value(ManagedSubscriptionProvidersContextKey{}).(map[auth.SubscriptionProvider]struct{})
	_, linked := enrolled[auth.SubscriptionProviderClaude]
	return present || linked || anthropicSubscriptionFromContext(ctx) != "", false
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
	return onEnterprise && claudeEnterpriseOnly(ctx)
}

// claudePlanQuotaRefusal reports a spent included-plan window with no overage
// serving. Usage-billed seats report no plan window, so only a plan seat that
// cannot serve the request matches.
func claudePlanQuotaRefusal(snapshot usage.Snapshot, now time.Time) bool {
	return snapshot.ExhaustedAsOf(now) && !snapshot.OverageInUse
}

func (s *Service) claudeLeaseRefusesPlanQuota(lease subscriptions.Lease, attemptErr error) bool {
	now := s.clockNow()
	var upstream *providers.UpstreamErrorResponse
	if errors.As(attemptErr, &upstream) {
		if snapshot, observed := usage.ParseAnthropicUnifiedHeaders(upstream.Headers); observed {
			snapshot.ObservedAt = now
			if claudePlanQuotaRefusal(snapshot, now) {
				return true
			}
			if snapshot.OverageInUse {
				return false
			}
		}
	}
	snapshot, observed := s.managedSubscriptionUsageSnapshot(lease.AccountID, lease.AccessToken)
	return observed && claudePlanQuotaRefusal(snapshot, now)
}

func markClaudePlanQuotaRefused(ctx context.Context) {
	if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); state != nil {
		state.ClaudePlanQuotaRefused = true
	}
}

func claudePoolModels(models []string) []string {
	var claudeModels []string
	for _, model := range models {
		if pool, compatible := subscriptionPoolForModel(inference.Target{CatalogID: model}); compatible && pool == subscriptions.ProviderClaude {
			claudeModels = append(claudeModels, model)
		}
	}
	return claudeModels
}
