package proxy

import (
	"context"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

func authoritativeUpgradeFor(s *Service, ctx context.Context, req router.Request, pin sessionpin.Pin, fresh router.Decision, res routingpolicy.TurnRoute) *routingpolicy.AuthoritativeUpgradeDecision {
	return routingpolicy.SessionTurn{
		Request: req, Fresh: fresh, PlannerTokens: 1000, Ports: s.strategyPorts,
		Settings: sessionSettingsFor(ctx, s), Now: s.clock.Now,
	}.UpgradeDecision(pin, res)
}

func hmmCostGatedDecision(ctx context.Context, s *Service, input continuity.CostGateInput) (router.Decision, planner.Decision, bool, string) {
	input.Planner = plannerConfigFor(ctx, s.continuityConfig.Planner)
	input.Now = s.clock.Now()
	return routingpolicy.CostGatedDecision(s.strategyPorts, hmmCostGateFor(s), input)
}
