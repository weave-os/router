package credentials

import (
	"context"
	"fmt"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions"
)

func (b *Brokerage) AuthorizeFunding(ctx, admissionCtx context.Context, attemptCreds *requestcontext.Credentials, target inference.Target, codexToken, claudeToken string) error {
	pool, compatible := SubscriptionPoolForModel(target)
	provider, model := target.Provider, target.CatalogID
	if billing.WeaveCreditsDeniedFromContext(ctx) && !CustomerFunded(attemptCreds) {
		return ErrWeaveCreditsDisabled
	}
	if ClaudeEnterpriseTarget(ctx, target) || ClaudeEnterpriseTurn(ctx) {
		return ClaudeEnterpriseFunding(ctx, target, b.SubscriptionTransport(target.Provider))
	}
	if QuotaProofExempt(ctx, attemptCreds) {
		return nil
	}
	if ByokFirst(ctx, target.Provider) {
		return nil
	}
	if b.claudePlanUnverified(ctx, pool, compatible) {
		observability.FromContext(ctx).Warn("Blocked paid recovery while Claude subscription identity is unverified", "provider", provider, "model", model)
		return fmt.Errorf("%w: Claude subscription plan could not be verified before paid fallback", ErrSubscriptionPoolUnavailable)
	}
	if !compatible {
		return nil
	}
	if PickedModelOnOwnPlan(ctx) {
		return SubscriptionPoolUnavailableBecause(ctx, "picked model runs on the caller's own plan")
	}
	refusal := ClientAppRefusalFunding{paidFallbackCounts: ClientAppRefusalPaidFallback(ctx), poolRefused: ClaudePoolRefusesClientApp(ctx), clientApp: requestcontext.ClientIdentityFrom(ctx).ClientApp}
	if err := b.AuthorizeSubscriptionAPI(admissionCtx, pool, target, codexToken, claudeToken, refusal); err != nil {
		observability.FromContext(ctx).Warn("Blocked API inference without subscription quota-exhaustion evidence", "model", model, "provider", provider, "subscription_provider", pool, "err", err)
		return err
	}
	return nil
}

func (b *Brokerage) claudePlanUnverified(ctx context.Context, pool subscriptions.Provider, compatible bool) bool {
	claudeTurn, _ := ctx.Value(ClaudeSubscriptionTurnKey{}).(bool)
	presentedPlan, _ := ctx.Value(ClaudeSubscriptionPlanKey{}).(providers.SubscriptionPlan)
	unverified := ClaudeSubscriptionPlan(ctx) == providers.SubscriptionPlanUnknown && (claudeTurn || compatible && pool == subscriptions.ProviderClaude) || claudeTurn && presentedPlan == providers.SubscriptionPlanUnknown
	if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); state != nil {
		unverified = unverified || state.ClaudePlanUnverified
	}
	return ClaudeEnterpriseEnabled(ctx) && unverified && !b.triedSubscriptionFallsBackToCredits(ctx)
}
