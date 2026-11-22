package proxy

import (
	"context"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/policyshadow"
)

func (s *Service) firePolicyShadowForServingDecision(ctx context.Context, strategy router.Strategy, serving router.Decision, req router.Request) {
	req = s.withPolicyRequestContext(ctx, req)
	shadowStrategy, _ := ctx.Value(requestcontext.PolicyShadowStrategyContextKey{}).(router.Strategy)
	installationID, _ := ctx.Value(requestcontext.InstallationIDContextKey{}).(string)
	store, _ := s.turnTelemetry.repo.(telemetry.PolicyShadowStore)
	policyshadow.Recorder{Store: store, Now: s.clock.Now, Route: s.routeWithStrategy}.Fire(ctx, installationID, strategy, shadowStrategy, serving, req)
}
