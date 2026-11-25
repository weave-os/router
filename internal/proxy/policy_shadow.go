package proxy

import (
	"context"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"

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
	fireRecipeShadow(ctx, s.turnTelemetry, s.clock, installationID, strategy, serving, req)
}

func fireRecipeShadow(ctx context.Context, tel turnTelemetry, clock auth.Clock, installationID string, strategy router.Strategy, serving router.Decision, req router.Request) {
	if stored, found := flags.StoredOverridesFromContext(ctx); tel.recipe.Replayer == nil || !found || !stored.Bools[flags.KeyRecipeShadow] {
		return
	}
	if model := router.StrategySelectionFromContext(ctx).RoutingModel; model != "" && model != router.ModelAutomatic {
		return
	}
	identity, managed := requestcontext.ServingIdentityFromContext(ctx)
	if !managed {
		return
	}
	recorder := tel.recipe
	recorder.Store, _ = tel.repo.(telemetry.PolicyShadowStore)
	recorder.Now = clock.Now
	recorder.Fire(ctx, policyshadow.RecipeTurn{InstallationID: installationID, ServingStrategy: strategy, ServingProfileKey: identity.ProfileKey, Served: serving, Request: req})
}
