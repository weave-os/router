package proxy

import (
	"context"
	"net/http"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
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
	if usage, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); usage != nil && usage.Finished {
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

func byokFirst(ctx context.Context, provider string) bool {
	keyFirst := false
	for _, key := range credentials.ExternalKeysFromContext(ctx) {
		if key.Provider == provider && (len(key.Plaintext) > 0 || key.CredentialError != nil) {
			keyFirst = key.FundingPriority == auth.FundingPriorityKeyFirst
		}
	}
	return keyFirst && !billing.ByokFeeUnpayable(ctx)
}

func anthropicBYOKFirst(ctx context.Context) bool {
	return byokFirst(ctx, providers.ProviderAnthropic)
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
