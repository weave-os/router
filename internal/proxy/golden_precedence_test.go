package proxy_test

import (
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

const (
	precedenceProbeTurn = `{"model":"claude-sonnet-4-5","max_tokens":1,"messages":[{"role":"user","content":"Say hi"}]}`
	precedenceRecapText = "The user stepped away and is coming back. Recap in under 40 words what happened."
)

// precedenceStrategySetup registers the scripted policy router under a
// non-HMM strategy, so the trace shows what pins and the planner do to its decisions.
type precedenceStrategySetup struct {
	strategy router.Strategy
	turns    [][]router.Decision
	options  []func(*proxy.Service) *proxy.Service
}

func (p precedenceStrategySetup) configure(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	svc = svc.WithPolicyStrategy(policy.StrategySpec{
		Strategy: p.strategy,
		Router:   &scriptedPolicyRouter{rec: h.rec, turns: p.turns},
	})
	for _, option := range p.options {
		svc = option(svc)
	}
	return svc
}

func precedenceStrategyDecision(strategy router.Strategy, model string) router.Decision {
	return router.Decision{Provider: providers.ProviderAnthropic, Model: model, Reason: string(strategy) + "_policy",
		Metadata: &router.RoutingMetadata{Strategy: string(strategy)}}
}

func precedenceStrategyScenario(strategy router.Strategy, name string, options []func(*proxy.Service) *proxy.Service, models []string, bodies ...string) goldenScenario {
	turns := make([]router.Decision, 0, len(models))
	for _, model := range models {
		turns = append(turns, precedenceStrategyDecision(strategy, model))
	}
	requests := make([]goldenRequest, 0, len(bodies))
	for _, body := range bodies {
		requests = append(requests, routingPolicyRequest(body, routingPolicyStrategy(strategy)))
	}
	setup := precedenceStrategySetup{strategy: strategy, turns: routingPolicyTurns(turns...), options: options}
	return goldenScenario{name: "routing_policy_" + string(strategy) + "_strategy_" + name, ingress: ingressAnthropic,
		upstreams: routingPolicyUpstreams(), requests: requests, configure: setup.configure}
}

// routingPolicyPrecedenceScenarios cover the turn-loop precedence rules the session pin
// scenarios do not reach, and the non-HMM strategies' pin and planner behaviour.
func routingPolicyPrecedenceScenarios() []goldenScenario {
	first, next := goldenRequest{body: routingPolicyFirstTurn}, goldenRequest{body: routingPolicyFollowUp("Now say bye")}
	explicitHardPin := []func(*proxy.Service) *proxy.Service{func(s *proxy.Service) *proxy.Service { return s.WithExplicitUtilityHardPin(true) }}
	plannerOff := []func(*proxy.Service) *proxy.Service{func(s *proxy.Service) *proxy.Service { return s.WithPlannerEnabled(false) }}
	scenarios := []goldenScenario{
		routingPolicyScenario("utility_probe_without_explicit_hard_pin_serves_requested_model", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyHigh))},
			first, goldenRequest{body: precedenceProbeTurn}),
		routingPolicyScenario("utility_probe_explicit_hard_pin_outranks_session_pin", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyHigh)), options: explicitHardPin},
			first, goldenRequest{body: precedenceProbeTurn}),
		routingPolicyScenario("force_model_outranks_utility_probe_hard_pin", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow))},
			goldenRequest{body: routingPolicyFirstTurn, headers: routingPolicyForce(routingPolicyHigh)}, goldenRequest{body: precedenceProbeTurn}),
		routingPolicyScenario("recap_turn_scored_without_session_pin", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyHigh), routingPolicyAnthropic(routingPolicyLow))},
			first, goldenRequest{body: routingPolicyFollowUp(precedenceRecapText)}),
	}
	for _, strategy := range []router.Strategy{router.StrategyRL, router.StrategyDecisionModel} {
		scenarios = append(scenarios,
			precedenceStrategyScenario(strategy, "first_turn_writes_session_pin", nil, []string{routingPolicyLow}, first.body),
			precedenceStrategyScenario(strategy, "planner_same_model_refreshes_pin", nil, []string{routingPolicyLow}, first.body, next.body, next.body),
			precedenceStrategyScenario(strategy, "planner_tier_upgrade_switches", nil, []string{routingPolicyLow, routingPolicyHigh}, first.body, next.body),
			precedenceStrategyScenario(strategy, "planner_tier_downgrade_on_warm_pin", nil, []string{routingPolicyHigh, routingPolicyLow}, first.body, next.body),
			precedenceStrategyScenario(strategy, "planner_disabled_keeps_first_decision", plannerOff, []string{routingPolicyLow, routingPolicyHigh}, first.body, next.body),
			precedenceStrategyScenario(strategy, "tool_result_rescored", nil, []string{routingPolicyLow, routingPolicyHigh}, first.body, routingPolicyToolResultTurn),
		)
	}
	return scenarios
}
