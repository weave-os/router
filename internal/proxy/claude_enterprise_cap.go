package proxy

import (
	"context"
	"errors"
	"net/http"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

// claudeEnterpriseCapRefused reports a 429 in which the seat refused both its
// included usage and its overage. Plain rate limits lack both rejections.
func claudeEnterpriseCapRefused(err error) bool {
	var upstream *providers.UpstreamErrorResponse
	return errors.As(err, &upstream) && upstream.Status == http.StatusTooManyRequests && usage.AnthropicOverageCapRejected(upstream.Headers)
}

// claudeEnterpriseCapRescue reports whether this turn's Enterprise seat hit
// its overage cap and the customer's plan-first Anthropic key can take over.
// Weave credits never fund it.
func claudeEnterpriseCapRescue(ctx context.Context) bool {
	state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	return state != nil && state.ClaudeEnterpriseCapRefused && usableBYOK(ctx, providers.ProviderAnthropic) != nil
}

// claudeEnterpriseFunding authorizes an Enterprise-only Claude turn on the
// caller's own seat, or on the customer's Anthropic key after the seat's
// overage-cap refusal.
func claudeEnterpriseFunding(ctx context.Context, target inference.Target, subscriptionTransport bool) error {
	creds := CredentialsFromContext(ctx)
	if target.Provider == providers.ProviderAnthropic && creds != nil && creds.Source == credSourceBYOK && creds.CredentialError == nil && claudeEnterpriseCapRescue(ctx) {
		return nil
	}
	pool, compatible := subscriptionPoolForModel(target)
	if creds == nil || !creds.OAuth || creds.Source != credSourceSubscription || !compatible || pool != subscriptions.ProviderClaude || !subscriptionTransport {
		return ErrClaudeEnterpriseSubscriptionUnavailable
	}
	return nil
}

// claudeEnterpriseCapRotation moves an Enterprise-only Claude turn off the
// caller's seat onto the customer's plan-first Anthropic key after the seat
// refused it at its overage cap.
func claudeEnterpriseCapRotation(credentialCtx, attemptCtx context.Context, decision router.Decision, attemptErr error) (context.Context, bool) {
	if !servedOnSubscription(credentialCtx) || !claudeEnterpriseTurn(credentialCtx) || !claudeEnterpriseCapRefused(attemptErr) {
		return attemptCtx, false
	}
	state, _ := credentialCtx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	if state == nil || usableBYOK(credentialCtx, providers.ProviderAnthropic) == nil {
		return attemptCtx, false
	}
	state.ClaudeEnterpriseCapRefused = true
	return resolveAndInjectCredentials(withSuppressedClaudeSubscription(attemptCtx), decision.Provider, decision.Model, http.Header{}), true
}
