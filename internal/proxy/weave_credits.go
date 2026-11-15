package proxy

import (
	"context"
	"errors"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
)

var ErrWeaveCreditsDisabled = errors.New("Weave credits are disabled and no customer plan or provider key can serve this request")

const forcedModelUnfundedReason = "Weave credits are turned off for your organization, and none of your plans or provider API keys can serve the forced model"

type weaveCreditsFundedKey struct{}

func weaveCreditsDenied(ctx context.Context) bool {
	return billing.WeaveCreditsDeniedFromContext(ctx)
}

func withWeaveCreditsFunding(ctx context.Context, headers http.Header) context.Context {
	if !weaveCreditsDenied(ctx) {
		return ctx
	}
	funded := subscriptionServableProviders(ctx, headers)
	for provider := range BuildCredentialsMap(externalKeysFromContext(ctx)) {
		if usableBYOK(ctx, provider) != nil {
			funded[provider] = struct{}{}
		}
	}
	return context.WithValue(ctx, weaveCreditsFundedKey{}, funded)
}

func weaveCreditsFundedProviders(ctx context.Context) (funded map[string]struct{}, ok bool) {
	funded, ok = ctx.Value(weaveCreditsFundedKey{}).(map[string]struct{})
	return funded, ok
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
	creds := BuildCredentialsMap(externalKeysFromContext(ctx))[provider]
	if creds == nil || creds.CredentialError != nil || len(creds.APIKey) == 0 {
		return nil
	}
	return creds
}

func customerFunded(creds *Credentials) bool {
	if creds == nil || creds.CredentialError != nil || len(creds.APIKey) == 0 {
		return false
	}
	return creds.OAuth || creds.Source == credSourceBYOK || creds.Source == credSourceClient
}

func withPaidFallbackCredentials(ctx context.Context, provider string) context.Context {
	if weaveCreditsDenied(ctx) || provider == providers.ProviderAnthropic && claudeEnterpriseCapRescue(ctx) {
		if byok := usableBYOK(ctx, provider); byok != nil {
			return context.WithValue(ctx, CredentialsContextKey{}, byok)
		}
	}
	return clearCredentials(ctx)
}
