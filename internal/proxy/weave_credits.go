package proxy

import (
	"context"
	"errors"
	"net/http"
)

// ErrWeaveCreditsDisabled refuses a turn that only Weave's own provider keys
// could serve while the installation does not allow Weave credits.
var ErrWeaveCreditsDisabled = errors.New("Weave credits are disabled and no customer plan or provider key can serve this request")

type weaveCreditsDeniedKey struct{}

// WithWeaveCreditsDenied marks a request whose installation does not let
// Weave's own provider keys, paid from Weave credits, fund inference.
func WithWeaveCreditsDenied(ctx context.Context) context.Context {
	return context.WithValue(ctx, weaveCreditsDeniedKey{}, true)
}

func weaveCreditsDenied(ctx context.Context) bool {
	denied, _ := ctx.Value(weaveCreditsDeniedKey{}).(bool)
	return denied
}

// restrictToCustomerFundedProviders keeps the providers a customer funding
// source can pay for: a connected provider key, or a Claude/ChatGPT plan the
// caller presents or has linked. Dispatch still checks the final credential,
// since a plan may not cover every model of its provider.
func restrictToCustomerFundedProviders(ctx context.Context, headers http.Header, enabled map[string]struct{}) map[string]struct{} {
	funded := subscriptionServableProviders(ctx, headers)
	for provider := range BuildCredentialsMap(externalKeysFromContext(ctx)) {
		funded[provider] = struct{}{}
	}
	out := make(map[string]struct{}, len(funded))
	for provider := range enabled {
		if _, ok := funded[provider]; ok {
			out[provider] = struct{}{}
		}
	}
	return out
}

// customerFunded reports whether creds are the customer's own: a provider key,
// a client-supplied key, or a subscription login. Nil creds mean the
// deployment key, which Weave credits pay for.
func customerFunded(creds *Credentials) bool {
	return creds != nil && (creds.OAuth || creds.Source == credSourceBYOK || creds.Source == credSourceClient)
}

// WeaveCreditsDeniedError returns ErrWeaveCreditsDisabled when the request's
// installation does not allow Weave credits, for callers outside this package.
func WeaveCreditsDeniedError(ctx context.Context) error {
	if weaveCreditsDenied(ctx) {
		return ErrWeaveCreditsDisabled
	}
	return nil
}
