package proxy

import (
	"context"
	"weave-os/router/internal/routingpolicy"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/breaker"

	"github.com/google/uuid"
)

func (s *Service) WithProviderOverloaded(enabled bool, registry *breaker.Registry) *Service {
	s.throttling.Overload = dispatch.NewOverloadBreaker(enabled, registry)
	return s
}

func (s *Service) overloadOpenModels(ctx context.Context) map[string]struct{} {
	return s.throttling.Overload.OpenModels(ctx, overloadOrg(ctx))
}

func overloadOrg(ctx context.Context) string {
	if id := installationIDFromContext(ctx); id != uuid.Nil {
		return id.String()
	}
	return ""
}

func (s *Service) claimOverloadProbe(ctx context.Context, req router.Request) (breaker.Probe, func(), bool) {
	if !s.throttling.Overload.Armed(ctx) || !s.overloadProbeRescuable(ctx, req) {
		return breaker.Probe{}, nil, false
	}
	org := overloadOrg(ctx)
	if org == "" {
		return breaker.Probe{}, nil, false
	}
	eligibility := req
	eligibility.Candidates.AutomaticExcludedModels = s.controlPlaneExcludedModels(ctx)
	return s.throttling.Overload.ClaimProbe(ctx, org, func(model string) bool {
		return !modelExcludedForRecoveryProbe(eligibility, model)
	})
}

func (s *Service) withTurnOverloadProbe(ctx context.Context, res routingpolicy.TurnRoute) context.Context {
	if res.OverloadProbe == nil {
		return ctx
	}
	if res.Decision.Model != res.OverloadProbe.Model {
		s.throttling.Overload.ReleaseProbe(*res.OverloadProbe)
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
	s.throttling.Overload.RecordOutcome(ctx, overloadOrg(ctx), model, provider, committed, err)
}

func withOverloadProbe(ctx context.Context, probe breaker.Probe) context.Context {
	return dispatch.WithOverloadProbe(ctx, probe)
}
