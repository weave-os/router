package proxy

import (
	"context"
	"errors"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/breaker"
)

// overloadBreakerReason is the disable reason reported for a model whose
// overload circuit is open, alongside the control plane's operator notes.
const overloadBreakerReason = "provider_overloaded"

// Deployment defaults for the breaker thresholds
// (ROUTER_PROVIDER_OVERLOADED_{STRIKES,WINDOW_SECONDS,RECOVERY_SECONDS,MAX_RECOVERY_SECONDS}).
const (
	DefaultOverloadStrikes            = 3
	DefaultOverloadWindowSeconds      = 120
	DefaultOverloadRecoverySeconds    = 30
	DefaultOverloadMaxRecoverySeconds = 600
)

type overloadProbeContextKey struct{}

// WithOverloadBreaker wires the per-model 529 circuit breaker
// (ROUTER_PROVIDER_OVERLOADED). Nil leaves overloads to the in-turn rescue.
func (s *Service) WithOverloadBreaker(registry *breaker.Registry) *Service {
	s.overloadBreaker = registry
	return s
}

// overloadOpenModels is the breaker's share of the deployment-wide automatic
// exclusion set: every open or half-open model except the one this request
// holds the probe for.
func (s *Service) overloadOpenModels(ctx context.Context) map[string]struct{} {
	if s.overloadBreaker == nil || !s.ResolveProviderOverloaded(ctx) {
		return nil
	}
	open := s.overloadBreaker.OpenModels()
	probe, held := ctx.Value(overloadProbeContextKey{}).(breaker.Probe)
	if _, excluded := open[probe.Model]; !held || !excluded {
		return open
	}
	readmitted := make(map[string]struct{}, len(open)-1)
	for model := range open {
		if model != probe.Model {
			readmitted[model] = struct{}{}
		}
	}
	return readmitted
}

// claimOverloadProbe hands this request the half-open slot of one due circuit
// the request could actually route to, and readmits that model for it alone.
// The release runs with the turn's other probe releases and is a no-op once
// the dispatch outcome has resolved the circuit.
func (s *Service) claimOverloadProbe(ctx context.Context, req router.Request) (context.Context, func(), bool) {
	if s.overloadBreaker == nil || !s.ResolveProviderOverloaded(ctx) || !s.overloadProbeRescuable(ctx, req) {
		return ctx, nil, false
	}
	eligibility := req
	eligibility.AutomaticExcludedModels = s.controlPlaneExcludedModels(ctx)
	probe, transition, ok := s.overloadBreaker.ClaimProbe(func(model string) bool {
		return !modelExcludedForRecoveryProbe(eligibility, model)
	})
	if !ok {
		return ctx, nil, false
	}
	logOverloadTransition(ctx, transition)
	release := func() { s.overloadBreaker.ReleaseProbe(probe) }
	return context.WithValue(ctx, overloadProbeContextKey{}, probe), release, true
}

// overloadProbeRescuable reports whether a 529 on the probe would be absorbed
// by the sibling rescue instead of reaching the client: the Gemini ingress
// never rescues, and the others only under the conditions their siblingViable
// checks share.
func (s *Service) overloadProbeRescuable(ctx context.Context, req router.Request) bool {
	return req.TranslationRequirements.Endpoint != router.EndpointGeminiGenerate &&
		s.ResolveSiblingFailover(ctx) &&
		s.shouldFailover(ctx) &&
		(!paidFallbackForbidden(ctx) || subscriptionStateModelsEnabled(ctx))
}

// recordOverloadOutcome feeds one dispatch operation's result to the breaker:
// an operation that exhausted on an upstream 529 strikes model, one that
// served it closes a half-open circuit. Anything else is no evidence either
// way. BYOK gateway capacity belongs to the tenant's endpoint, not the model,
// so a gateway-served operation never counts.
func (s *Service) recordOverloadOutcome(ctx context.Context, model, provider string, committed bool, err error) {
	if s.overloadBreaker == nil || model == "" || providers.IsGateway(provider) || !s.ResolveProviderOverloaded(ctx) {
		return
	}
	if err == nil {
		logOverloadTransition(ctx, s.overloadBreaker.RecordServed(model))
		return
	}
	if committed || errors.Is(err, context.Canceled) || !isProviderOverloaded(err) {
		return
	}
	logOverloadTransition(ctx, s.overloadBreaker.RecordOverload(model))
}

// isProviderOverloaded reports a buffered upstream 529, the status Anthropic's
// overloaded_error maps to.
func isProviderOverloaded(err error) bool {
	var buffered *providers.UpstreamErrorResponse
	return errors.As(err, &buffered) && buffered.Status == providerOverloadedStatus
}

func logOverloadTransition(ctx context.Context, t breaker.Transition) {
	if !t.Changed() {
		return
	}
	log := observability.FromContext(ctx).Info
	if t.To == breaker.StateHalfOpen {
		// A due slot can be claimed and released by many requests that end up
		// serving another model; only the outcome transitions are news.
		log = observability.FromContext(ctx).Debug
	}
	log("Overload circuit changed state",
		"model", t.Model,
		"from", string(t.From),
		"to", string(t.To),
		"overload_failures", t.Failures,
		"recovery_after_ms", t.RecoveryAfter.Milliseconds(),
	)
}
