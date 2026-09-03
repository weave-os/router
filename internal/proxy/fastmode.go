package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"workweave/router/internal/observability/otel"
	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/catalog"
	"workweave/router/internal/translate"
)

// anthropicTierAttempt dispatches a prepared Anthropic-native body, re-emitting
// it for any binding whose fast-tier eligibility differs from the one the body
// was built with (first-party ↔ gateway, or onto a subscription) so no attempt
// carries the other tier's speed field or is billed at the wrong rate.
type anthropicTierAttempt struct {
	s             *Service
	log           *slog.Logger
	env           *translate.RequestEnvelope
	r             *http.Request
	opts          translate.EmitOptions
	native        dispatchAttempt
	sink          http.ResponseWriter
	preludeBuf    *preludeBuffer
	marker        string
	setExtractor  func(*otel.UsageExtractor)
	setStreamCost func(router.Decision, bool)
	logBody       func(router.Decision, []byte)
}

// forBinding returns the emit options and native attempt to dispatch d with,
// plus whether that dispatch is fast.
func (a *anthropicTierAttempt) forBinding(actx context.Context, d router.Decision) (translate.EmitOptions, dispatchAttempt, bool, error) {
	fast := fastModeForAttempt(actx, d.Model, d.Provider)
	if fast == a.opts.FastMode {
		return a.opts, a.native, fast, nil
	}
	attemptOpts := a.opts
	attemptOpts.TargetProvider = d.Provider
	attemptOpts.FastMode = fast
	prep, err := a.env.PrepareAnthropic(a.r.Header, attemptOpts)
	if err != nil {
		a.log.Error("Failed to re-emit Anthropic body for fast-tier change", "err", err)
		return attemptOpts, nil, fast, fmt.Errorf("emit body: %w", err)
	}
	a.logBody(d, prep.Body)
	native := a.s.anthropicNativeAttempt(a.env, a.r, prep, a.sink, a.preludeBuf, a.marker, a.setExtractor, a.setStreamCost)
	return attemptOpts, native, fast, nil
}

// attempt is forBinding as a dispatchAttempt; recordFast receives the tier of
// every attempt so the caller's fastServed reflects the winning one.
func (a *anthropicTierAttempt) attempt(recordFast func(bool)) dispatchAttempt {
	return func(actx context.Context, d router.Decision, p providers.Client) error {
		_, native, fast, err := a.forBinding(actx, d)
		recordFast(fast)
		if err != nil {
			return err
		}
		return native(actx, d, p)
	}
}

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
