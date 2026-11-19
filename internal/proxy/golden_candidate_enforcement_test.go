package proxy_test

import (
	"context"
	"strings"
	"sync"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
)

// candidateBlindRouter answers its n-th routing call with the n-th scripted
// decision without consulting any candidate list, standing in for a strategy
// that ignores filtering so the serving layer's membership check is exercised.
type candidateBlindRouter struct {
	rec   *goldenRecorder
	turns []router.Decision
	mu    sync.Mutex
	calls int
}

func (r *candidateBlindRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	r.mu.Lock()
	d := r.turns[min(r.calls, len(r.turns)-1)]
	r.calls++
	r.mu.Unlock()
	r.rec.mu.Lock()
	defer r.rec.mu.Unlock()
	r.rec.routes = append(r.rec.routes, goldenRouteCall{
		RequestedModel: req.RequestedModel, Excluded: routingPolicySorted(req.Candidates.ExcludedModels),
		AutomaticExcluded: routingPolicySorted(req.Candidates.AutomaticExcludedModels),
		Model:             d.Model, Provider: d.Provider, Reason: d.Reason,
		Request: goldenValue(candidateEnforcementTraceOf(req.Candidates)),
	})
	return d, nil
}

type candidateEnforcementTrace struct {
	SafetyExcludedModels     []string
	ImageLimitExcludedModels []string
}

func candidateEnforcementTraceOf(c router.Candidates) candidateEnforcementTrace {
	return candidateEnforcementTrace{
		SafetyExcludedModels:     routingPolicySorted(c.SafetyExcludedModels),
		ImageLimitExcludedModels: routingPolicySorted(c.ImageLimitExcludedModels),
	}
}

// candidateBlindPick returns a decision for model served by provider, with fallback ranked after it; every fallback is served by Anthropic.
func candidateBlindPick(strategy router.Strategy, provider, model string, fallback ...string) router.Decision {
	providersByModel := map[string]string{model: provider}
	for _, m := range fallback {
		if _, known := providersByModel[m]; !known {
			providersByModel[m] = providers.ProviderAnthropic
		}
	}
	return router.Decision{Provider: provider, Model: model, Reason: "golden_route", Metadata: &router.RoutingMetadata{
		Strategy: string(strategy), CandidateModels: append([]string{model}, fallback...), CandidateProviders: providersByModel,
	}}
}

// candidateRosterPick limits decision's ranked fallback to a rescue roster that
// holds only the pick, leaving its other candidates outside the roster.
func candidateRosterPick(decision router.Decision) router.Decision {
	decision.Metadata.RosterFailover = true
	decision.Metadata.RescueModels = []string{decision.Model}
	return decision
}

func candidateBlindSetup(strategy router.Strategy, session *routingPolicySession, available map[string]struct{}, turns ...router.Decision) func(*proxy.Service, *goldenHarness) *proxy.Service {
	return func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
		if session != nil {
			session.h = h
		}
		if available != nil {
			svc = svc.WithAvailableModels(available)
		}
		return svc.WithPolicyStrategy(policy.StrategySpec{Strategy: strategy, Router: &candidateBlindRouter{rec: h.rec, turns: turns}})
	}
}

func candidateEnforcementImageTurn(images int) string {
	image := `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}`
	return `{"model":"claude-sonnet-4-5","max_tokens":4096,"messages":[{"role":"user","content":[` +
		strings.Repeat(image+",", images) + `{"type":"text","text":"What are these?"}]}]}`
}

func goldenCandidateEnforcementScenarios() []goldenScenario {
	first, next := goldenRequest{body: routingPolicyFirstTurn}, goldenRequest{body: routingPolicyFollowUp("continue")}
	maxedOutThenOK := map[string][]goldenStep{providers.ProviderAnthropic: {routingPolicyMaxedOut(), anthropicOK(false, "ok")}}
	hmm, rl := router.StrategyHMMEmbedding, router.StrategyRL
	rlFirst, rlNext := routingPolicyRequest(first.body, routingPolicyStrategy(rl)), routingPolicyRequest(next.body, routingPolicyStrategy(rl))
	demoted, demotedOnly := &routingPolicySession{}, &routingPolicySession{}
	demote := func(s *routingPolicySession) goldenRequest {
		return routingPolicyRequest(next.body, s.editPins("default_mid", func(p *sessionpin.Pin) { p.DemotedModels = []string{routingPolicyMid} }))
	}
	imageUpstreams := map[string][]goldenStep{
		providers.ProviderAnthropic: {anthropicOK(false, "ok")},
		providers.ProviderDeepInfra: {compatOKWithModel(false, "XiaomiMiMo/MiMo-V2.6-Pro", "ok")},
	}
	servedMid := routingPolicyAnthropic(routingPolicyMid)
	servedLow := routingPolicyAnthropic(routingPolicyLow)
	imageAvailable := map[string]struct{}{routingPolicyLow: {}, routingPolicyMid: {}, catalog.ModelMiMoV26Pro: {}}
	return []goldenScenario{
		{name: "candidate_enforcement_hmm_pick_safety_excluded", ingress: ingressAnthropic, upstreams: maxedOutThenOK,
			configure: candidateBlindSetup(hmm, nil, nil, servedMid, candidateBlindPick(hmm, providers.ProviderAnthropic, routingPolicyMid, routingPolicyLow)),
			requests:  []goldenRequest{first, next}},
		{name: "candidate_enforcement_hmm_pick_safety_excluded_without_eligible_fallback", ingress: ingressAnthropic, upstreams: maxedOutThenOK,
			configure: candidateBlindSetup(hmm, nil, nil, servedMid, candidateBlindPick(hmm, providers.ProviderAnthropic, routingPolicyMid)),
			requests:  []goldenRequest{first, {body: next.body, noTelemetry: true}}},
		{name: "candidate_enforcement_hmm_pick_safety_excluded_refused_inside_rescue_roster", ingress: ingressAnthropic, upstreams: maxedOutThenOK,
			configure: candidateBlindSetup(hmm, nil, nil, servedMid, candidateRosterPick(candidateBlindPick(hmm, providers.ProviderAnthropic, routingPolicyMid, routingPolicyLow))),
			requests:  []goldenRequest{first, {body: next.body, noTelemetry: true}}},
		{name: "candidate_enforcement_rl_pick_safety_excluded", ingress: ingressAnthropic, upstreams: maxedOutThenOK,
			configure: candidateBlindSetup(rl, nil, nil, servedMid, candidateBlindPick(rl, providers.ProviderAnthropic, routingPolicyMid, routingPolicyLow)),
			requests:  []goldenRequest{rlFirst, rlNext}},
		{name: "candidate_enforcement_image_pick_image_limited", ingress: ingressAnthropic, upstreams: imageUpstreams,
			configure: candidateBlindSetup(hmm, nil, imageAvailable, candidateBlindPick(hmm, providers.ProviderDeepInfra, catalog.ModelMiMoV26Pro, routingPolicyLow)),
			requests:  []goldenRequest{{body: candidateEnforcementImageTurn(5)}}},
		{name: "candidate_enforcement_automatic_pick_automatic_excluded", ingress: ingressAnthropic, upstreams: routingPolicyUpstreams(),
			configure: candidateBlindSetup(hmm, demoted, nil, servedLow, candidateBlindPick(hmm, providers.ProviderAnthropic, routingPolicyMid, routingPolicyLow)),
			requests:  []goldenRequest{first, demote(demoted)}},
		{name: "candidate_enforcement_automatic_pick_kept_when_every_fallback_automatic_excluded", ingress: ingressAnthropic, upstreams: routingPolicyUpstreams(),
			configure: candidateBlindSetup(hmm, demotedOnly, nil, servedLow, candidateBlindPick(hmm, providers.ProviderAnthropic, routingPolicyMid)),
			requests:  []goldenRequest{first, demote(demotedOnly)}},
	}
}
