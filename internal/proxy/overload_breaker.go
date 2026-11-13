package proxy

import (
	"context"
	"errors"
	"maps"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/breaker"

	"github.com/google/uuid"
)

// overloadBreakerReason is the disable reason reported for a model whose
// overload circuit is open, alongside the control plane's operator notes.
const overloadBreakerReason = "provider_overloaded"

type overloadProbeContextKey struct{}

// WithProviderOverloaded sets the deployment default for the per-model 529
// circuit breaker (ROUTER_PROVIDER_OVERLOADED) and the replica's registry.
// The registry is wired even when the default is off so a per-org override
// can enable it; nil leaves overloads to the in-turn rescue.
func (s *Service) WithProviderOverloaded(enabled bool, registry *breaker.Registry) *Service {
	s.providerOverloaded, s.overloadBreaker = enabled, registry
	return s
}

// overloadOpenModels is the breaker's share of the deployment-wide automatic
// exclusion set for this request's organization: open shared circuits it has
// confirmed with its own overload failures, plus its own open gateway
// circuits, except the model this request holds the probe for. A request with
// no organization sees nothing.
func (s *Service) overloadOpenModels(ctx context.Context) map[string]struct{} {
	org := overloadOrg(ctx)
	if s.overloadBreaker == nil || org == "" || !s.ResolveProviderOverloaded(ctx) {
		return nil
	}
	withdrawn := s.overloadBreaker.Withdrawn(org)
	probe, held := ctx.Value(overloadProbeContextKey{}).(breaker.Probe)
	if _, excluded := withdrawn[probe.Model]; !held || !excluded {
		return withdrawn
	}
	readmitted := maps.Clone(withdrawn)
	delete(readmitted, probe.Model)
	return readmitted
}

// overloadOrg keys the breaker by the request's installation ID. It returns ""
// for a request with no installation, which the breaker never records or withdraws for.
func overloadOrg(ctx context.Context) string {
	if id := installationIDFromContext(ctx); id != uuid.Nil {
		return id.String()
	}
	return ""
}

// claimOverloadProbe hands this request the half-open slot of one due circuit
// the request could actually route to, and readmits that model for it alone.
// The release runs with the turn's other probe releases and is a no-op once
// the dispatch outcome has resolved the circuit.
func (s *Service) claimOverloadProbe(ctx context.Context, req router.Request) (breaker.Probe, func(), bool) {
	if s.overloadBreaker == nil || !s.ResolveProviderOverloaded(ctx) || !s.overloadProbeRescuable(ctx, req) {
		return breaker.Probe{}, nil, false
	}
	org := overloadOrg(ctx)
	if org == "" {
		return breaker.Probe{}, nil, false
	}
	eligibility := req
	eligibility.AutomaticExcludedModels = s.controlPlaneExcludedModels(ctx)
	probe, transition, ok := s.overloadBreaker.ClaimProbe(org, func(model string) bool {
		return !modelExcludedForRecoveryProbe(eligibility, model)
	})
	if !ok {
		return breaker.Probe{}, nil, false
	}
	logOverloadTransition(ctx, transition)
	release := func() { s.overloadBreaker.ReleaseProbe(probe) }
	return probe, release, true
}

// withOverloadProbe marks ctx as holding probe, readmitting its model for this
// request and letting the dispatch outcome resolve the circuit.
func withOverloadProbe(ctx context.Context, probe breaker.Probe) context.Context {
	return context.WithValue(ctx, overloadProbeContextKey{}, probe)
}

// withTurnOverloadProbe carries a probe claimed during routing onto the
// ingress's dispatch context when the turn routed to the probed model, and
// otherwise frees the slot now so the next request can probe instead of
// waiting for this one to finish streaming another model.
func (s *Service) withTurnOverloadProbe(ctx context.Context, res turnLoopResult) context.Context {
	if res.OverloadProbe == nil {
		return ctx
	}
	if res.Decision.Model != res.OverloadProbe.Model {
		s.overloadBreaker.ReleaseProbe(*res.OverloadProbe)
		return ctx
	}
	return withOverloadProbe(ctx, *res.OverloadProbe)
}

// overloadProbeRescuable reports whether a 529 on the probe would be absorbed
// by the sibling rescue instead of reaching the client: the Gemini ingress
// and caller-model passthrough never rescue, and the others only when
// siblingRescueAllowed holds. A gateway organization's rescue walks its own
// gateway aliases, so its requests may probe its isolated circuits.
func (s *Service) overloadProbeRescuable(ctx context.Context, req router.Request) bool {
	return req.TranslationRequirements.Endpoint != router.EndpointGeminiGenerate &&
		!auth.RoutingPassthroughFrom(ctx) &&
		s.siblingRescueAllowed(ctx, len(s.gatewayProvidersForRequest(ctx)) > 0)
}

// recordOverloadOutcome feeds one dispatch operation's result to the breaker.
// provider is the binding that produced the outcome: a 529 through the
// organization's own gateway is evidence about that endpoint only and goes to
// its isolated circuit, while a 529 straight from a provider is evidence about
// the provider's capacity and goes to the shared circuit, attributed to the
// organization so it can confirm. For the model this request holds the probe
// for, a served operation closes the circuit and a 529 reopens it; otherwise a
// served operation changes nothing, so a slow request that began before the
// circuit opened cannot close it. Anything else is no evidence either way.
func (s *Service) recordOverloadOutcome(ctx context.Context, model, provider string, committed bool, err error) {
	org := overloadOrg(ctx)
	if s.overloadBreaker == nil || org == "" || !s.ResolveProviderOverloaded(ctx) {
		return
	}
	overloaded := err != nil && !committed && !errors.Is(err, context.Canceled) && isProviderOverloaded(err)
	if probe, held := ctx.Value(overloadProbeContextKey{}).(breaker.Probe); held && probe.Model == model {
		switch {
		case err == nil:
			logOverloadTransition(ctx, s.overloadBreaker.ProbeSucceeded(probe))
		case overloaded:
			logOverloadTransition(ctx, s.overloadBreaker.ProbeFailed(probe))
		}
		return
	}
	if overloaded {
		logOverloadTransition(ctx, s.overloadBreaker.RecordOverload(org, model, providers.IsGateway(provider)))
	}
}

// isProviderOverloaded reports a buffered upstream 529, the status Anthropic's
// overloaded_error maps to.
func isProviderOverloaded(err error) bool {
	var buffered *providers.UpstreamErrorResponse
	return errors.As(err, &buffered) && buffered.Status == providerOverloadedStatus
}

func logOverloadTransition(ctx context.Context, transition breaker.Transition) {
	if !transition.Changed() {
		return
	}
	log := observability.FromContext(ctx).Info
	if transition.To == breaker.StateHalfOpen {
		// A due slot can be claimed and released by many requests that end up
		// serving another model; only the outcome transitions are news.
		log = observability.FromContext(ctx).Debug
	}
	log("Overload circuit changed state",
		"model", transition.Model,
		"installation_id", transition.Org,
		"isolated", transition.Isolated,
		"from", string(transition.From),
		"to", string(transition.To),
		"org_confirmed", transition.Confirmed,
		"recovery_after_ms", transition.RecoveryAfter.Milliseconds(),
	)
}
