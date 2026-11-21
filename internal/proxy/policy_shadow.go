package proxy

import (
	"context"

	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/policyshadow"
)

func (s *Service) firePolicyShadowForServingDecision(ctx context.Context, strategy router.Strategy, serving router.Decision, req router.Request) {
	req = s.withPolicyRequestContext(ctx, req)
	shadowStrategy, _ := ctx.Value(PolicyShadowStrategyContextKey{}).(router.Strategy)
	installationID, _ := ctx.Value(InstallationIDContextKey{}).(string)
	store, _ := s.telemetry.(telemetry.PolicyShadowStore)
	recorder := policyshadow.Recorder{Store: store, Now: s.clock.Now, Route: s.routeWithStrategy}
	recorder.Fire(ctx, installationID, strategy, shadowStrategy, serving, req)
}
