package proxy

import (
	"context"
	"net/http"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

const (
	credSourceBYOK                = requestcontext.SourceBYOK
	credSourceClient              = requestcontext.SourceClient
	credSourceSubscription        = requestcontext.SourceSubscription
	credSourceSubscriptionOverage = requestcontext.SourceSubscriptionOverage
	credSourceCodexSubscription   = requestcontext.SourceCodexSubscription
)

type Credentials = requestcontext.Credentials

type CredentialsContextKey = requestcontext.CredentialsContextKey

func CredentialsFromContext(ctx context.Context) *Credentials {
	if override, set := ctx.Value(CredentialsContextKey{}).(*Credentials); set && override == nil {
		return nil
	}
	if usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); usage != nil && usage.Finished {
		return usage.WinningCredentials
	}
	return requestcontext.CredentialsFromContext(ctx)
}

func EffectiveBaseURL(ctx context.Context, fallback string) string {
	return requestcontext.EffectiveBaseURL(ctx, fallback)
}

func EffectiveUpstreamModel(ctx context.Context, model string) string {
	return requestcontext.EffectiveUpstreamModel(ctx, model)
}

func ApplyModelAlias(ctx context.Context, body []byte, model string) []byte {
	return requestcontext.ApplyModelAlias(ctx, body, model)
}

func ApplyIdentityHeader(ctx context.Context, upstream *http.Request) {
	requestcontext.ApplyIdentityHeader(ctx, upstream)
}

type ExternalAPIKeysContextKey struct{}

func byokFirst(ctx context.Context, provider string) bool {
	return credentials.KeyFirst(externalKeysFromContext(ctx), provider)
}

func anthropicBYOKFirst(ctx context.Context) bool {
	return byokFirst(ctx, providers.ProviderAnthropic)
}

func BuildCredentialsMap(keys []*auth.ExternalAPIKey) map[string]*Credentials {
	return credentials.BuildCredentialsMap(keys)
}

func ExtractClientCredentials(provider string, headers http.Header) *Credentials {
	return requestcontext.ExtractClientCredentials(provider, headers)
}

func subscriptionCredsFromToken(token string) *Credentials {
	return requestcontext.SubscriptionCredsFromToken(strings.TrimSpace(token))
}

func codexSubscriptionCreds(token, accountID string) *Credentials {
	return requestcontext.CodexSubscriptionCreds(token, accountID)
}

func clearCredentials(ctx context.Context) context.Context {
	return context.WithValue(ctx, CredentialsContextKey{}, (*Credentials)(nil))
}

func resolveAndInjectCredentials(ctx context.Context, provider, model string, headers http.Header) context.Context {
	subDisabled := subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx)
	resolution := credentials.Resolve(requestFactsFrom(ctx, headers), provider, credentials.Suppression{
		Claude: claudeSubscriptionSuppressed(ctx) || subDisabled || claudeModelSuppressed(ctx, model),
		Codex:  codexSubscriptionSuppressed(ctx) || codexModelSuppressed(ctx, model) || codexChatEndpoint(ctx) || subDisabled || !codexSubscriptionCanAttemptModel(model),
	})
	if resolution.Credentials == nil {
		if resolution.Clear {
			return clearCredentials(ctx)
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
	return context.WithValue(ctx, CredentialsContextKey{}, resolution.Credentials)
}
