package proxy_test

import (
	"context"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/rl"
)

type goldenRLDecider struct{}

func (goldenRLDecider) Decide(_ context.Context, query rl.Query) (rl.Result, error) {
	model := "anthropic/" + routingPolicyHigh
	if query.TurnIndex > 0 {
		model = "anthropic/claude-haiku-4.5"
	}
	return rl.Result{Model: model}, nil
}

func goldenRLTurnIndexScenario() goldenScenario {
	return goldenScenario{
		name: "rl_turn_index", ingress: ingressAnthropic, upstreams: routingPolicyUpstreams(),
		requests: []goldenRequest{
			routingPolicyRequest(routingPolicyFirstTurn, routingPolicyStrategy(router.StrategyRL)),
			routingPolicyRequest(routingPolicyFollowUp("Say hi again"), routingPolicyStrategy(router.StrategyRL)),
		},
		configure: func(svc *proxy.Service, _ *goldenHarness) *proxy.Service {
			deployed := map[string]struct{}{routingPolicyHigh: {}, routingPolicyLow: {}}
			available := map[string]struct{}{providers.ProviderAnthropic: {}}
			return svc.WithPolicyStrategy(policy.StrategySpec{
				Strategy: router.StrategyRL,
				Router:   rl.New(goldenRLDecider{}, deployed, available),
			})
		},
	}
}
