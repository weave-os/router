package proxy

import (
	"context"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
)

// betaProvidersDisabledForRequest returns every beta provider the request's
// organization has not opted into. Beta flags have no deployment default:
// only a per-organization override admits the provider, so an unset flag
// reads as off even on a deployment that holds the provider's key.
func betaProvidersDisabledForRequest(ctx context.Context) map[string]struct{} {
	var out map[string]struct{}
	for provider, key := range providers.BetaProviderFlags {
		if flags.BoolOr(ctx, key, false) {
			continue
		}
		if out == nil {
			out = make(map[string]struct{}, len(providers.BetaProviderFlags))
		}
		out[provider] = struct{}{}
	}
	return out
}
