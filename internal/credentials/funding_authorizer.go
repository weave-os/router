package credentials

import (
	"context"
	"fmt"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions"
)

type FundingChecks struct {
	ClaudeEnterprise         func(ctx context.Context) (applies bool, err error)
	ByokFirst                func(ctx context.Context) bool
	PlanUnverified           func(ctx context.Context) bool
	PickedModelOnOwnPlan     func(ctx context.Context) bool
	AuthorizeSubscriptionAPI func(ctx context.Context) error
}

func AuthorizeFunding(ctx context.Context, attemptCreds *requestcontext.Credentials, provider, model string, pool subscriptions.Provider, compatible bool, checks FundingChecks) error {
	if billing.WeaveCreditsDeniedFromContext(ctx) && !CustomerFunded(attemptCreds) {
		return ErrWeaveCreditsDisabled
	}
	if applies, err := checks.ClaudeEnterprise(ctx); applies {
		return err
	}
	if QuotaProofExempt(ctx, attemptCreds) {
		return nil
	}
	if checks.ByokFirst(ctx) {
		return nil
	}
	if checks.PlanUnverified(ctx) {
		observability.FromContext(ctx).Warn("Blocked paid recovery while Claude subscription identity is unverified", "provider", provider, "model", model)
		return fmt.Errorf("%w: Claude subscription plan could not be verified before paid fallback", ErrSubscriptionPoolUnavailable)
	}
	if !compatible {
		return nil
	}
	if checks.PickedModelOnOwnPlan(ctx) {
		return SubscriptionPoolUnavailableBecause(ctx, "picked model runs on the caller's own plan")
	}
	if err := checks.AuthorizeSubscriptionAPI(ctx); err != nil {
		observability.FromContext(ctx).Warn("Blocked API inference without subscription quota-exhaustion evidence", "model", model, "provider", provider, "subscription_provider", pool, "err", err)
		return err
	}
	return nil
}
