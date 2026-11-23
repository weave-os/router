package credentials

import (
	"context"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions"
)

func SubscriptionServableProviders(ctx context.Context, headers http.Header) map[string]struct{} {
	codex, anthropic := PresentSubscriptionTokens(ctx, headers)
	out := make(map[string]struct{}, 2)
	if codex != "" || ManagedSubscriptionEnrolled(ctx, subscriptions.ProviderCodex) {
		out[providers.ProviderOpenAI] = struct{}{}
	}
	if anthropic != "" || ManagedSubscriptionEnrolled(ctx, subscriptions.ProviderClaude) {
		out[providers.ProviderAnthropic] = struct{}{}
	}
	return out
}

const ForcedModelUnfundedReason = "Weave credits are turned off for your organization, and none of your plans or provider API keys can serve the forced model"

type weaveCreditsFundedKey struct{}

func WithWeaveCreditsFunding(ctx context.Context, headers http.Header) context.Context {
	if !billing.WeaveCreditsDeniedFromContext(ctx) {
		return ctx
	}
	return context.WithValue(ctx, weaveCreditsFundedKey{}, CustomerFundedProviders(ctx, headers))
}

func CustomerFundedProviders(ctx context.Context, headers http.Header) map[string]struct{} {
	funded := SubscriptionServableProviders(ctx, headers)
	for provider := range BuildCredentialsMap(ExternalKeysFromContext(ctx)) {
		if UsableBYOK(ctx, provider) != nil {
			funded[provider] = struct{}{}
		}
	}
	return funded
}

func WeaveCreditsFundedProviders(ctx context.Context) (funded map[string]struct{}, ok bool) {
	funded, ok = ctx.Value(weaveCreditsFundedKey{}).(map[string]struct{})
	return funded, ok
}

func WeaveCreditsCannotFund(ctx context.Context, provider string) bool {
	funded, denied := WeaveCreditsFundedProviders(ctx)
	if !denied {
		return false
	}
	_, ok := funded[provider]
	return !ok
}

func ApplyWeaveCreditsPolicy(ctx context.Context, enabled map[string]struct{}) (map[string]struct{}, error) {
	funded, ok := WeaveCreditsFundedProviders(ctx)
	if !ok {
		return enabled, nil
	}
	out := make(map[string]struct{}, len(funded))
	for provider := range enabled {
		if _, ok := funded[provider]; ok {
			out[provider] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil, ErrWeaveCreditsDisabled
	}
	return out, nil
}

func UsableBYOK(ctx context.Context, provider string) *requestcontext.Credentials {
	creds := BuildCredentialsMap(ExternalKeysFromContext(ctx))[provider]
	if creds == nil || creds.CredentialError != nil || len(creds.APIKey) == 0 {
		return nil
	}
	return creds
}

func ServedOnCustomerKey(ctx context.Context) bool {
	return CustomerKeyFunded(CredentialsFromContext(ctx))
}

func WithPaidFallbackCredentials(ctx context.Context, provider string) context.Context {
	if billing.WeaveCreditsDeniedFromContext(ctx) || provider == providers.ProviderAnthropic && ClaudeEnterpriseCapRescue(ctx) {
		if byok := UsableBYOK(ctx, provider); byok != nil {
			return context.WithValue(ctx, requestcontext.CredentialsContextKey{}, byok)
		}
	}
	return ClearCredentials(ctx)
}
