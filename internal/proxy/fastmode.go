package proxy

import (
	"context"
	"slices"

	"workweave/router/internal/router/catalog"
)

// fastModeForAttempt reports whether a dispatch of model on provider goes out
// on the provider's fast tier. True only when the installation opted the model
// in, the (provider, model) binding publishes a fast rate, and the resolved
// credential is not a subscription OAuth token — Weave does not bill
// subscription-served turns, so it must not burn the caller's plan at the fast
// rate. Evaluated per attempt against the attempt's own ctx and binding so a
// failover to a gateway (no fast tier) or onto a subscription drops it.
func fastModeForAttempt(ctx context.Context, model, provider string) bool {
	if !slices.Contains(installationFastModeModelsFromContext(ctx), model) {
		return false
	}
	if _, ok := catalog.FastPriceFor(provider, model); !ok {
		return false
	}
	return !servedOnSubscription(ctx)
}

// servedPricing returns the rate the winning attempt is billed at: the fast
// rate when it was dispatched fast, else the binding's list price, else the
// primary binding's list price.
func servedPricing(provider, model string, fast bool) (catalog.Pricing, bool) {
	if fast {
		if fastPrice, ok := catalog.FastPriceFor(provider, model); ok {
			return fastPrice, true
		}
	}
	if price, ok := catalog.PriceFor(provider, model); ok {
		return price, true
	}
	return catalog.PrimaryPriceFor(model)
}
