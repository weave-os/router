package proxy_test

import (
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/policy"
)

const (
	precedenceProbeTurn = `{"model":"claude-sonnet-4-5","max_tokens":1,"messages":[{"role":"user","content":"Say hi"}]}`
	precedenceRecapText = "The user stepped away and is coming back. Recap in under 40 words what happened."
)

func precedenceStrategyScenario(strategy router.Strategy, name string, options []func(*proxy.Service) *proxy.Service, models []string, bodies ...string) goldenScenario {
	turns := make([]router.Decision, 0, len(models))
	for _, model := range models {
		turns = append(turns, router.Decision{Provider: providers.ProviderAnthropic, Model: model, Reason: string(strategy) + "_policy",
			Metadata: &router.RoutingMetadata{Strategy: string(strategy)}})
	}
	requests := make([]goldenRequest, 0, len(bodies))
	for _, body := range bodies {
		requests = append(requests, routingPolicyRequest(body, routingPolicyStrategy(strategy)))
	}
	configure := func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
		svc = svc.WithPolicyStrategy(policy.StrategySpec{Strategy: strategy, Router: &scriptedPolicyRouter{rec: h.rec, turns: routingPolicyTurns(turns...)}})
		for _, option := range options {
			svc = option(svc)
		}
		return svc
	}
	return goldenScenario{name: "routing_policy_" + string(strategy) + "_strategy_" + name, ingress: ingressAnthropic,
		upstreams: routingPolicyUpstreams(), requests: requests, configure: configure}
}

// routingPolicyPrecedenceScenarios cover the turn-loop precedence rules the session pin
// scenarios do not reach, and the non-HMM strategies' pin and planner behaviour.
func routingPolicyPrecedenceScenarios() []goldenScenario {
	first, followUp := goldenRequest{body: routingPolicyFirstTurn}, goldenRequest{body: routingPolicyFollowUp("Now say bye")}
	scenarios := []goldenScenario{
		routingPolicyScenario("utility_probe_without_explicit_hard_pin_serves_requested_model", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyHigh))},
			first, goldenRequest{body: precedenceProbeTurn}),
		routingPolicyScenario("utility_probe_explicit_hard_pin_outranks_session_pin", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyHigh)), options: []func(*proxy.Service) *proxy.Service{func(s *proxy.Service) *proxy.Service { return s.WithExplicitUtilityHardPin(true) }}},
			first, goldenRequest{body: precedenceProbeTurn}),
		routingPolicyScenario("force_model_outranks_utility_probe_hard_pin", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow))},
			goldenRequest{body: routingPolicyFirstTurn, headers: routingPolicyForce(routingPolicyHigh)}, goldenRequest{body: precedenceProbeTurn}),
		routingPolicyScenario("recap_turn_scored_without_session_pin", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyHigh), routingPolicyAnthropic(routingPolicyLow))},
			first, goldenRequest{body: routingPolicyFollowUp(precedenceRecapText)}),
	}
	for _, strategy := range []router.Strategy{router.StrategyRetiredRL, router.StrategyDecisionModel} {
		scenarios = append(scenarios,
			precedenceStrategyScenario(strategy, "first_turn_writes_session_pin", nil, []string{routingPolicyLow}, first.body),
			precedenceStrategyScenario(strategy, "planner_same_model_refreshes_pin", nil, []string{routingPolicyLow}, first.body, followUp.body, followUp.body),
			precedenceStrategyScenario(strategy, "planner_tier_upgrade_switches", nil, []string{routingPolicyLow, routingPolicyHigh}, first.body, followUp.body),
			precedenceStrategyScenario(strategy, "planner_tier_downgrade_on_warm_pin", nil, []string{routingPolicyHigh, routingPolicyLow}, first.body, followUp.body),
			precedenceStrategyScenario(strategy, "planner_disabled_keeps_first_decision",
				[]func(*proxy.Service) *proxy.Service{func(s *proxy.Service) *proxy.Service {
					return s.EditContinuityConfig(func(c *continuity.Config) { c.PlannerEnabled = false })
				}}, []string{routingPolicyLow, routingPolicyHigh}, first.body, followUp.body),
			precedenceStrategyScenario(strategy, "tool_result_rescored", nil, []string{routingPolicyLow, routingPolicyHigh}, first.body, routingPolicyToolResultTurn),
		)
	}
	return scenarios
}
