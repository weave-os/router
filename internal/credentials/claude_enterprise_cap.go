package credentials

import (
	"context"
	"errors"
	"net/http"

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

func ClaudeEnterpriseFunding(ctx context.Context, target inference.Target, subscriptionTransport bool) error {
	creds := CredentialsFromContext(ctx)
	if target.Provider == providers.ProviderAnthropic && creds != nil && creds.Source == requestcontext.SourceBYOK && creds.CredentialError == nil && ClaudeEnterpriseCapRescue(ctx) {
		return nil
	}
	pool, compatible := SubscriptionPoolForModel(target)
	if creds == nil || !creds.OAuth || creds.Source != requestcontext.SourceSubscription || !compatible || pool != subscriptions.ProviderClaude || !subscriptionTransport {
		return ClaudeEnterpriseRefusal(ctx)
	}
	return nil
}

func ClaudeEnterpriseCapRotation(credentialCtx, attemptCtx context.Context, decision router.Decision, attemptErr error) (context.Context, bool) {
	if !ServedOnSubscription(credentialCtx) || !ClaudeEnterpriseTurn(credentialCtx) || !claudeEnterpriseCapRefused(attemptErr) {
		return attemptCtx, false
	}
	state, _ := credentialCtx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	if state == nil || UsableBYOK(credentialCtx, providers.ProviderAnthropic) == nil {
		return attemptCtx, false
	}
	state.ClaudeEnterpriseCapRefused = true
	return ResolveAndInjectCredentials(WithSuppressedClaudeSubscription(attemptCtx), decision.Provider, decision.Model, http.Header{}), true
}

func BypassMayReroute(ctx context.Context, err error) bool {
	return !ClaudeEnterpriseOnly(ctx) || claudeEnterpriseCapRefused(err) && UsableBYOK(ctx, providers.ProviderAnthropic) != nil
}
