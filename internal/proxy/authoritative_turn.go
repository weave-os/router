package proxy

import (
	"context"

	"github.com/google/uuid"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"
)

type authoritativeTurn struct {
	env            *translate.RequestEnvelope
	feats          translate.RoutingFeatures
	strategy       router.Strategy
	req            router.Request
	pin            sessionpin.Pin
	pinFound       bool
	switchHistory  sessionpin.Pin
	fresh          router.Decision
	prefixBroken   bool
	sessionPins    routingpolicy.SessionPins
	installationID uuid.UUID
}

func serveAuthoritativeTurn(ctx context.Context, s *Service, res *routingpolicy.TurnRoute, in authoritativeTurn) (routingpolicy.TurnRoute, error) {
	return routingpolicy.AuthoritativeTurn{
		Request: in.req, Pin: in.pin, PinFound: in.pinFound, SwitchHistory: in.switchHistory, Fresh: in.fresh, PrefixBroken: in.prefixBroken,
		PlannerTokens: s.plannerTokensFor(ctx, in.env, in.feats), Pins: in.sessionPins, InstallationID: in.installationID,
		Ports: s.strategyPorts, Scorer: strategyScorer{s: s, strategy: in.strategy}, Settings: authoritativeSettingsFor(ctx, s), Now: s.clock.Now,
	}.Serve(ctx, res)
}

func authoritativeSettingsFor(ctx context.Context, s *Service) routingpolicy.AuthoritativeSettings {
	return routingpolicy.AuthoritativeSettings{
		UpgradePolicy:                  s.ResolveAuthoritativeUpgradePolicy(ctx),
		UpgradeGate:                    s.ResolveAuthoritativeUpgradeGate(ctx),
		UpgradeVotes:                   s.ResolveAuthoritativeUpgradeVotes(ctx),
		DowngradeGate:                  s.ResolveAuthoritativeDowngradeGate(ctx),
		DowngradeHysteresisTurns:       s.ResolveHMMDowngradeHysteresisTurns(ctx),
		DowngradeHysteresisShadowTurns: s.ResolveHMMDowngradeHysteresisShadowTurns(ctx),
		CacheShadow:                    s.ResolveAuthorityCacheShadow(ctx),
		Config:                         s.hmmConfig,
		AvailableModels:                s.routing.AvailableModels(),
		CostGate:                       hmmCostGateFor(s),
		Planner:                        plannerConfigFor(ctx, s.continuityConfig.Planner),
	}
}

type strategyScorer struct {
	s        *Service
	strategy router.Strategy
}

func (r strategyScorer) Route(ctx context.Context, req router.Request) (router.Decision, error) {
	return r.s.routeFor(ctx, r.strategy, req)
}
