package proxy

import (
	"context"
	"errors"
	"net/http"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

func claudeEnterpriseCapRefused(err error) bool {
	var upstream *providers.UpstreamErrorResponse
	return errors.As(err, &upstream) && upstream.Status == http.StatusTooManyRequests && usage.AnthropicOverageCapRejected(upstream.Headers)
}

func claudeEnterpriseCapRescue(ctx context.Context) bool {
	state, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage)
	return state != nil && state.ClaudeEnterpriseCapRefused && usableBYOK(ctx, providers.ProviderAnthropic) != nil
}

func claudeEnterpriseFunding(ctx context.Context, target inference.Target, subscriptionTransport bool) error {
	creds := credentials.CredentialsFromContext(ctx)
	if target.Provider == providers.ProviderAnthropic && creds != nil && creds.Source == requestcontext.SourceBYOK && creds.CredentialError == nil && claudeEnterpriseCapRescue(ctx) {
		return nil
	}
	pool, compatible := subscriptionPoolForModel(target)
	if creds == nil || !creds.OAuth || creds.Source != requestcontext.SourceSubscription || !compatible || pool != subscriptions.ProviderClaude || !subscriptionTransport {
		return claudeEnterpriseRefusal(ctx)
	}
	return nil
}

func claudeEnterpriseCapRotation(credentialCtx, attemptCtx context.Context, decision router.Decision, attemptErr error) (context.Context, bool) {
	if !credentials.ServedOnSubscription(credentialCtx) || !claudeEnterpriseTurn(credentialCtx) || !claudeEnterpriseCapRefused(attemptErr) {
		return attemptCtx, false
	}
	state, _ := credentialCtx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage)
	if state == nil || usableBYOK(credentialCtx, providers.ProviderAnthropic) == nil {
		return attemptCtx, false
	}
	state.ClaudeEnterpriseCapRefused = true
	return credentials.ResolveAndInjectCredentials(credentials.WithSuppressedClaudeSubscription(attemptCtx), decision.Provider, decision.Model, http.Header{}), true
}

func bypassMayReroute(ctx context.Context, err error) bool {
	return !claudeEnterpriseOnly(ctx) || claudeEnterpriseCapRefused(err) && usableBYOK(ctx, providers.ProviderAnthropic) != nil
}
