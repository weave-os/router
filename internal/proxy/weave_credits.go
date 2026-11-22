package proxy

import (
	"context"
	"errors"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
)

var ErrWeaveCreditsDisabled = errors.New("weave credits are disabled and no customer plan or provider key can serve this request")

const forcedModelUnfundedReason = "Weave credits are turned off for your organization, and none of your plans or provider API keys can serve the forced model"

type weaveCreditsFundedKey struct{}

func withWeaveCreditsFunding(ctx context.Context, headers http.Header) context.Context {
	if !billing.WeaveCreditsDeniedFromContext(ctx) {
		return ctx
	}
	return context.WithValue(ctx, weaveCreditsFundedKey{}, customerFundedProviders(ctx, headers))
}

func customerFundedProviders(ctx context.Context, headers http.Header) map[string]struct{} {
	funded := subscriptionServableProviders(ctx, headers)
	for provider := range credentials.BuildCredentialsMap(credentials.ExternalKeysFromContext(ctx)) {
		if usableBYOK(ctx, provider) != nil {
			funded[provider] = struct{}{}
		}
	}
	return funded
}

func weaveCreditsFundedProviders(ctx context.Context) (funded map[string]struct{}, ok bool) {
	funded, ok = ctx.Value(weaveCreditsFundedKey{}).(map[string]struct{})
	return funded, ok
}

func weaveCreditsCannotFund(ctx context.Context, provider string) bool {
	funded, denied := weaveCreditsFundedProviders(ctx)
	if !denied {
		return false
	}
	_, ok := funded[provider]
	return !ok
}

func applyWeaveCreditsPolicy(ctx context.Context, enabled map[string]struct{}) (map[string]struct{}, error) {
	funded, ok := weaveCreditsFundedProviders(ctx)
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

func usableBYOK(ctx context.Context, provider string) *Credentials {
	creds := credentials.BuildCredentialsMap(credentials.ExternalKeysFromContext(ctx))[provider]
	if creds == nil || creds.CredentialError != nil || len(creds.APIKey) == 0 {
		return nil
	}
	return creds
}

func servedOnCustomerKey(ctx context.Context) bool {
	return credentials.CustomerKeyFunded(CredentialsFromContext(ctx))
}

func withPaidFallbackCredentials(ctx context.Context, provider string) context.Context {
	if billing.WeaveCreditsDeniedFromContext(ctx) || provider == providers.ProviderAnthropic && claudeEnterpriseCapRescue(ctx) {
		if byok := usableBYOK(ctx, provider); byok != nil {
			return context.WithValue(ctx, CredentialsContextKey{}, byok)
		}
	}
	return clearCredentials(ctx)
}
