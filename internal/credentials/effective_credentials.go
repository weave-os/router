package credentials

import (
	"context"
	"net/http"
	"strings"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

func CredentialsFromContext(ctx context.Context) *requestcontext.Credentials {
	if override, set := ctx.Value(requestcontext.CredentialsContextKey{}).(*requestcontext.Credentials); set && override == nil {
		return nil
	}
	if usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); usage != nil && usage.Finished {
		return usage.WinningCredentials
	}
	return requestcontext.CredentialsFromContext(ctx)
}

func ByokFirst(ctx context.Context, provider string) bool {
	return KeyFirst(ExternalKeysFromContext(ctx), provider) && !billing.ByokFeeUnpayable(ctx)
}

func AnthropicBYOKFirst(ctx context.Context) bool {
	return ByokFirst(ctx, providers.ProviderAnthropic)
}

func SubscriptionCredsFromToken(token string) *requestcontext.Credentials {
	return requestcontext.SubscriptionCredsFromToken(strings.TrimSpace(token))
}

func ClearCredentials(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestcontext.CredentialsContextKey{}, (*requestcontext.Credentials)(nil))
}

func ResolveAndInjectCredentials(ctx context.Context, provider, model string, headers http.Header) context.Context {
	subDisabled := SubscriptionRoutingDisabledForRequest(ctx) || SubscriptionFundingOutOfPlayForRequest(ctx)
	resolution := Resolve(RequestFactsFrom(ctx, headers), provider, Suppression{
		Claude: ClaudeSubscriptionSuppressed(ctx) || subDisabled || ClaudeModelSuppressed(ctx, model),
		Codex:  CodexSubscriptionSuppressed(ctx) || CodexModelSuppressed(ctx, model) || CodexChatEndpoint(ctx) || subDisabled || !requestcontext.CodexSubscriptionCanAttemptModel(model),
	})
	if resolution.Credentials == nil {
		if resolution.Clear {
			return ClearCredentials(ctx)
		}
		return ctx
	}
	if resolution.InboundSubscription {
		log := observability.FromContext(ctx)
		if provider == providers.ProviderAnthropic {
			log.Info("Resolved Claude subscription credential", "credential_source", resolution.Credentials.Source)
		} else {
			log.Debug("Resolved Codex subscription credential for OpenAI turn", "credential_source", resolution.Credentials.Source)
		}
	}
	return context.WithValue(ctx, requestcontext.CredentialsContextKey{}, resolution.Credentials)
}

func ManagedSubscriptionServed(ctx context.Context) bool {
	usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	return usage != nil && usage.Served
}

func ServedOnCodexSubscription(ctx context.Context) bool {
	creds := CredentialsFromContext(ctx)
	return creds != nil && creds.OAuth && len(creds.AccountID) > 0
}

func ServedOnSubscription(ctx context.Context) bool {
	creds := CredentialsFromContext(ctx)
	return (creds != nil && creds.OAuth) || ManagedSubscriptionServed(ctx)
}

func ServedOnBYOK(ctx context.Context) bool {
	creds := CredentialsFromContext(ctx)
	return creds != nil && creds.Source == requestcontext.SourceBYOK
}
