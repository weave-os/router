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

const overloadBreakerReason = "provider_overloaded"

type overloadProbeContextKey struct{}

func (s *Service) WithProviderOverloaded(enabled bool, registry *breaker.Registry) *Service {
	s.providerOverloaded, s.overloadBreaker = enabled, registry
	return s
}

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

func overloadOrg(ctx context.Context) string {
	if id := installationIDFromContext(ctx); id != uuid.Nil {
		return id.String()
	}
	return ""
}

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

func withOverloadProbe(ctx context.Context, probe breaker.Probe) context.Context {
	return context.WithValue(ctx, overloadProbeContextKey{}, probe)
}

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

func (s *Service) overloadProbeRescuable(ctx context.Context, req router.Request) bool {
	return req.TranslationRequirements.Endpoint != router.EndpointGeminiGenerate &&
		!auth.RoutingPassthroughFrom(ctx) &&
		s.siblingRescueAllowed(ctx, len(s.gatewayProvidersForRequest(ctx)) > 0)
}

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
