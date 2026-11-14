package proxy

import (
	"context"
	"errors"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
)

// ErrWeaveCreditsDisabled refuses a turn that only Weave's own provider keys
// could serve while the installation does not allow Weave credits.
var ErrWeaveCreditsDisabled = errors.New("Weave credits are disabled and no customer plan or provider key can serve this request")

// forcedModelUnfundedReason is the ForcedModelExcludedError reason for a
// forced model no customer funding source can pay for; it also unwraps to
// ErrWeaveCreditsDisabled.
const forcedModelUnfundedReason = "Weave credits are turned off for your organization, and none of your plans or provider API keys can serve the forced model"

type weaveCreditsFundedKey struct{}

func weaveCreditsDenied(ctx context.Context) bool {
	return billing.WeaveCreditsDeniedFromContext(ctx)
}

// withWeaveCreditsFunding records, for an installation that denies Weave
// credits, the providers a customer funding source can pay for: a usable
// connected provider key, or a Claude/ChatGPT plan the caller presents or has
// linked. A caller-sent API key is never forwarded on the router-key path, so
// it is not a funding source here.
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

// weaveCreditsFundedProviders returns the funded set recorded by
// withWeaveCreditsFunding, or ok=false when Weave credits are allowed.
func weaveCreditsFundedProviders(ctx context.Context) (funded map[string]struct{}, ok bool) {
	funded, ok = ctx.Value(weaveCreditsFundedKey{}).(map[string]struct{})
	return funded, ok
}

// applyWeaveCreditsPolicy narrows enabled to customer-funded providers.
// Dispatch still checks the final credential, since a plan may not cover
// every model of its provider.
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

// usableBYOK returns the customer's connected key for provider when it can
// authenticate an upstream call.
func usableBYOK(ctx context.Context, provider string) *Credentials {
	creds := BuildCredentialsMap(externalKeysFromContext(ctx))[provider]
	if creds == nil || creds.CredentialError != nil || len(creds.APIKey) == 0 {
		return nil
	}
	return creds
}

// customerFunded reports whether creds are the customer's own and usable: a
// provider key, a client-supplied key, or a subscription login. Nil creds
// mean the deployment key, which Weave credits pay for.
func customerFunded(creds *Credentials) bool {
	if creds == nil || creds.CredentialError != nil || len(creds.APIKey) == 0 {
		return false
	}
	return creds.OAuth || creds.Source == credSourceBYOK || creds.Source == credSourceClient
}

// withPaidFallbackCredentials attaches the credential a paid-fallback probe is judged
// on: the customer's usable provider key when Weave credits are denied or an
// Enterprise seat's overage cap hands the turn to that key, otherwise none
// (the deployment key).
func withPaidFallbackCredentials(ctx context.Context, provider string) context.Context {
	if weaveCreditsDenied(ctx) || provider == providers.ProviderAnthropic && claudeEnterpriseCapRescue(ctx) {
		if byok := usableBYOK(ctx, provider); byok != nil {
			return context.WithValue(ctx, CredentialsContextKey{}, byok)
		}
	}
	return clearCredentials(ctx)
}
