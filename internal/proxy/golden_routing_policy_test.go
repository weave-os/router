package proxy_test

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/bandswap"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

// Routing Policy models: a cheap, a mid and a top Anthropic tier, plus a
// text-only open model served by a compat provider.
const (
	routingPolicyLow      = "claude-haiku-4-5"
	routingPolicyMid      = "claude-sonnet-4-5"
	routingPolicyHigh     = "claude-opus-4-7"
	routingPolicyTextOnly = "z-ai/glm-5.1"
)

var routingPolicyAvailableModels = map[string]struct{}{routingPolicyLow: {}, routingPolicyMid: {}, routingPolicyHigh: {}, routingPolicyTextOnly: {}, goldenCompatModel: {}}

func routingPolicyFollowUp(text string) string {
	return `{"model":"claude-sonnet-4-5","max_tokens":4096,"messages":[{"role":"user","content":"Say hi"},` +
		`{"role":"assistant","content":"hi"},{"role":"user","content":"` + text + `"}]}`
}

const routingPolicyToolResultTurn = `{"model":"claude-sonnet-4-5","max_tokens":4096,
"tools":[{"name":"Bash","description":"run","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],
"messages":[
{"role":"user","content":"Say hi"},
{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"echo hi"}}]},
{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"hi"}]}]}`

const routingPolicyImageTurn = `{"model":"claude-sonnet-4-5","max_tokens":4096,"messages":[{"role":"user","content":"Say hi"},
{"role":"assistant","content":"hi"},
{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}},{"type":"text","text":"What is this?"}]}]}`

var routingPolicyFirstTurn = goldenAnthropicBody(false)

func routingPolicyMaxedOut() goldenStep {
	return goldenOK("application/json", []string{`{"id":"msg_up","type":"message","role":"assistant","model":"` + goldenAnthropicModel +
		`","content":[{"type":"text","text":"truncated"}],"stop_reason":"max_tokens","usage":{"input_tokens":12,"output_tokens":4096}}`})
}

func routingPolicyUpstreams() map[string][]goldenStep {
	return map[string][]goldenStep{
		providers.ProviderAnthropic: {anthropicOK(false, "ok")},
		providers.ProviderFireworks: {compatOKWithModel(false, "accounts/fireworks/models/glm-5p1", "ok")},
	}
}

func routingPolicyAnthropic(model string) router.Decision {
	return decision(providers.ProviderAnthropic, model)
}

// routingPolicyHMM is a non-authoritative HMM selection with a classifier confidence.
func routingPolicyHMM(model string, confidence float32) router.Decision {
	return routingPolicyHMMReason(model, "hmm_policy", confidence)
}

func routingPolicyHMMReason(model, reason string, confidence float32) router.Decision {
	return router.Decision{Provider: providers.ProviderAnthropic, Model: model, Reason: reason, Metadata: &router.RoutingMetadata{
		Strategy: string(router.StrategyHMMEmbedding), ChosenScore: confidence, PolicyGroup: "golden-group",
	}}
}

// routingPolicyBandPair is a scored decision that anchors a {model, paired} band.
func routingPolicyBandPair(model, paired string) router.Decision {
	d := routingPolicyAnthropic(model)
	d.Metadata = &router.RoutingMetadata{PairedModel: paired, PairedProvider: providers.ProviderAnthropic, Embedding: make([]float32, bandswap.EmbedDim)}
	return d
}

func routingPolicyTurns(turns ...router.Decision) [][]router.Decision {
	out := make([][]router.Decision, len(turns))
	for i, d := range turns {
		out[i] = []router.Decision{d}
	}
	return out
}

// scriptedPolicyRouter answers its n-th routing call from the n-th candidate
// list, so a scenario can script how the policy's choice moves across turns.
type scriptedPolicyRouter struct {
	rec          *goldenRecorder
	turns        [][]router.Decision
	traceRequest bool
	mu           sync.Mutex
	calls        int
}

func (r *scriptedPolicyRouter) Route(ctx context.Context, req router.Request) (router.Decision, error) {
	r.mu.Lock()
	turn := r.turns[min(r.calls, len(r.turns)-1)]
	r.calls++
	r.mu.Unlock()
	d, err := (&goldenRouter{decisions: turn, rec: r.rec}).Route(ctx, req)
	if r.traceRequest {
		r.rec.mu.Lock()
		r.rec.routes[len(r.rec.routes)-1].Request = goldenValue(routingPolicyRequestTraceOf(req))
		r.rec.mu.Unlock()
	}
	return d, err
}

type routingPolicyRequestTrace struct {
	EnabledProviders     []string
	AllowedModels        []string
	PreferredModels      []string
	CustomBindings       map[string][]string
	GatewayProviders     []string
	SafetyExcludedModels []string
	PreviousPolicyGroup  string
	ForceCluster         string
	PolicyTurnContext    *router.PolicyTurnContext
}

func routingPolicyRequestTraceOf(req router.Request) routingPolicyRequestTrace {
	return routingPolicyRequestTrace{
		EnabledProviders:     routingPolicySorted(req.EnabledProviders),
		AllowedModels:        routingPolicySorted(req.AllowedModels),
		PreferredModels:      req.PreferredModels,
		CustomBindings:       req.CustomBindings,
		GatewayProviders:     routingPolicySorted(req.GatewayProviders),
		SafetyExcludedModels: routingPolicySorted(req.SafetyExcludedModels),
		PreviousPolicyGroup:  string(req.PreviousPolicyGroup),
		ForceCluster:         req.ForceCluster,
		PolicyTurnContext:    req.PolicyTurnContext,
	}
}

func routingPolicySorted(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// routingPolicySession lets a later request edit the session state an earlier one wrote.
type routingPolicySession struct{ h *goldenHarness }

func (s *routingPolicySession) editPins(role string, edit func(*sessionpin.Pin)) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		s.h.rec.mu.Lock()
		defer s.h.rec.mu.Unlock()
		for k, p := range s.h.pins.pins {
			if p.Role == role {
				edit(&p)
				s.h.pins.pins[k] = p
			}
		}
		return ctx
	}
}

func routingPolicyExpire(p *sessionpin.Pin) { p.PinnedUntil = time.Now().Add(-time.Minute) }

func routingPolicyEvict(p *sessionpin.Pin) { p.PinnedUntil = goldenEpoch.Add(-time.Minute) }

type routingPolicySetup struct {
	turns   [][]router.Decision
	caps    policy.Capabilities
	trace   bool
	session *routingPolicySession
	options []func(*proxy.Service) *proxy.Service
}

func (p routingPolicySetup) configure(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	if p.session != nil {
		p.session.h = h
	}
	svc = svc.WithPolicyStrategy(policy.StrategySpec{
		Strategy:     router.StrategyHMMEmbedding,
		Router:       &scriptedPolicyRouter{rec: h.rec, turns: p.turns, traceRequest: p.trace},
		Capabilities: p.caps,
	})
	for _, option := range p.options {
		svc = option(svc)
	}
	return svc
}

func routingPolicyScenario(name string, setup routingPolicySetup, requests ...goldenRequest) goldenScenario {
	return goldenScenario{name: "routing_policy_" + name, ingress: ingressAnthropic, upstreams: routingPolicyUpstreams(),
		requests: requests, configure: setup.configure}
}

func routingPolicyCtx(decorators ...func(context.Context) context.Context) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		for _, d := range decorators {
			ctx = d(ctx)
		}
		return ctx
	}
}

func routingPolicyExcludeModels(models ...string) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		return context.WithValue(ctx, proxy.InstallationExcludedModelsContextKey{}, models)
	}
}

func routingPolicyAllowModels(models ...string) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		return context.WithValue(ctx, proxy.InstallationAllowedModelsContextKey{}, models)
	}
}

func routingPolicyPreferModels(models ...string) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		return context.WithValue(ctx, proxy.InstallationPreferredModelsContextKey{}, models)
	}
}

func routingPolicyExternalKeys(keys ...*auth.ExternalAPIKey) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		return context.WithValue(ctx, proxy.ExternalAPIKeysContextKey{}, keys)
	}
}

// excludeCompatProvider is the operator provider exclusion; a stored pin on
// that provider stands for one the installation has since stopped enabling.
func excludeCompatProvider(svc *proxy.Service) *proxy.Service {
	return svc.WithExcludedProvidersOverride([]string{providers.ProviderFireworks})
}

func routingPolicyMoveToCompat(reason string) func(*sessionpin.Pin) {
	return func(p *sessionpin.Pin) {
		p.Model, p.Provider, p.Reason = routingPolicyTextOnly, providers.ProviderFireworks, reason
	}
}

func routingPolicyStrategy(strategy router.Strategy) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context { return router.WithStrategy(ctx, strategy) }
}

func routingPolicyAuthorizedPin(ctx context.Context) context.Context {
	return router.WithPolicyPinRequest(ctx, router.PolicyPinRequest{Authorized: true, Pin: router.PolicyPin{
		ArtifactSHA256: strings.Repeat("a", 64), RosterSHA256: strings.Repeat("b", 64)}})
}

func routingPolicyRequest(body string, decorators ...func(context.Context) context.Context) goldenRequest {
	return goldenRequest{body: body, decorateContext: routingPolicyCtx(decorators...)}
}

func routingPolicyRefused(body string, headers map[string]string, decorators ...func(context.Context) context.Context) goldenRequest {
	return goldenRequest{body: body, headers: headers, decorateContext: routingPolicyCtx(decorators...), noTelemetry: true}
}

func routingPolicyForce(model string) map[string]string {
	return map[string]string{proxy.ForceModelHeader: model}
}

func withAvailable(svc *proxy.Service) *proxy.Service {
	return svc.WithAvailableModels(routingPolicyAvailableModels)
}

func goldenRoutingPolicyScenarios() []goldenScenario {
	return append(append(append(append(routingPolicySessionPinScenarios(), routingPolicyEscalationAndClassifierScenarios()...),
		routingPolicySelectionGateScenarios()...), routingPolicyForceModelScenarios()...), routingPolicyEligibilityScenarios()...)
}

// routingPolicySessionPinScenarios cover pin load, refresh, expiry and every guard that
// drops a stored pin before selection.
func routingPolicySessionPinScenarios() []goldenScenario {
	first, next := goldenRequest{body: routingPolicyFirstTurn}, goldenRequest{body: routingPolicyFollowUp("Now say bye")}
	expired, evicted, mismatched := &routingPolicySession{}, &routingPolicySession{}, &routingPolicySession{}
	disabled, demoted, providerNotEnabled := &routingPolicySession{}, &routingPolicySession{}, &routingPolicySession{}
	bandSwap := []func(*proxy.Service) *proxy.Service{withAvailable, func(s *proxy.Service) *proxy.Service { return s.WithBandSwap(true) }}
	return []goldenScenario{
		routingPolicyScenario("first_turn_writes_session_pin", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow))}, first),
		routingPolicyScenario("planner_disabled_keeps_first_decision", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow), routingPolicyAnthropic(routingPolicyHigh)),
			options: []func(*proxy.Service) *proxy.Service{func(s *proxy.Service) *proxy.Service { return s.WithPlannerEnabled(false) }}},
			first, next),
		routingPolicyScenario("planner_same_model_refreshes_pin", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow))}, first, next, next),
		routingPolicyScenario("planner_tier_upgrade_switches", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow), routingPolicyAnthropic(routingPolicyHigh))}, first, next),
		routingPolicyScenario("planner_tier_downgrade_on_warm_pin", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyHigh), routingPolicyAnthropic(routingPolicyLow))}, first, next),
		routingPolicyScenario("expired_pin_reanchors_prior_model", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyHigh), routingPolicyAnthropic(routingPolicyLow)),
			session: expired, options: []func(*proxy.Service) *proxy.Service{withAvailable}},
			first, routingPolicyRequest(next.body, expired.editPins("default_mid", routingPolicyExpire))),
		routingPolicyScenario("pin_absent_from_store_routes_as_first_turn", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyHigh), routingPolicyAnthropic(routingPolicyLow)),
			session: evicted, options: []func(*proxy.Service) *proxy.Service{withAvailable}},
			first, routingPolicyRequest(next.body, evicted.editPins("default_mid", routingPolicyEvict))),
		routingPolicyScenario("pin_from_other_strategy_ignored", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyHigh), routingPolicyAnthropic(routingPolicyLow)), session: mismatched},
			first, routingPolicyRequest(next.body, mismatched.editPins("default_mid", func(p *sessionpin.Pin) { p.Strategy = router.StrategyHMMBeta }))),
		routingPolicyScenario("pin_on_excluded_provider_falls_through", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow)), trace: true, session: providerNotEnabled,
			options: []func(*proxy.Service) *proxy.Service{excludeCompatProvider}},
			first, routingPolicyRequest(next.body, providerNotEnabled.editPins("default_mid", routingPolicyMoveToCompat("golden_route")))),
		routingPolicyScenario("pin_model_excluded_falls_through", routingPolicySetup{turns: [][]router.Decision{{routingPolicyAnthropic(routingPolicyLow)}, {routingPolicyAnthropic(routingPolicyLow), routingPolicyAnthropic(routingPolicyMid)}}},
			first, routingPolicyRequest(next.body, routingPolicyExcludeModels(routingPolicyLow))),
		routingPolicyScenario("pin_outside_installation_allowlist_falls_through", routingPolicySetup{turns: [][]router.Decision{{routingPolicyAnthropic(routingPolicyLow)}, {routingPolicyAnthropic(routingPolicyLow), routingPolicyAnthropic(routingPolicyMid)}},
			options: []func(*proxy.Service) *proxy.Service{withAvailable}},
			first, routingPolicyRequest(next.body, routingPolicyAllowModels(routingPolicyMid, routingPolicyHigh))),
		routingPolicyScenario("text_only_pin_dropped_for_image_turn", routingPolicySetup{turns: routingPolicyTurns(decision(providers.ProviderFireworks, routingPolicyTextOnly), routingPolicyAnthropic(routingPolicyHigh))},
			first, goldenRequest{body: routingPolicyImageTurn}),
		{name: "routing_policy_maxed_out_model_excluded_next_turn", ingress: ingressAnthropic,
			upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {routingPolicyMaxedOut(), anthropicOK(false, "ok")}},
			configure: routingPolicySetup{turns: [][]router.Decision{{routingPolicyAnthropic(routingPolicyLow)}, {routingPolicyAnthropic(routingPolicyLow), routingPolicyAnthropic(routingPolicyMid)}}}.configure,
			requests:  []goldenRequest{first, {body: routingPolicyFollowUp("continue")}}},
		routingPolicyScenario("tool_result_reuses_pin_when_scoring_off", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow), routingPolicyAnthropic(routingPolicyHigh)),
			options: []func(*proxy.Service) *proxy.Service{func(s *proxy.Service) *proxy.Service { return s.WithScoreToolResultTurns(false) }}},
			first, goldenRequest{body: routingPolicyToolResultTurn}),
		routingPolicyScenario("tool_result_rescored_by_default", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow), routingPolicyAnthropic(routingPolicyHigh))},
			first, goldenRequest{body: routingPolicyToolResultTurn}),
		routingPolicyScenario("session_disabled_provider_leaves_enabled_set", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow)), trace: true, session: disabled},
			first, routingPolicyRequest(next.body, disabled.editPins("default_mid", func(p *sessionpin.Pin) { p.DisabledProviders = []string{providers.ProviderFireworks} }))),
		routingPolicyScenario("session_demoted_model_automatically_excluded", routingPolicySetup{turns: [][]router.Decision{{routingPolicyAnthropic(routingPolicyLow)}, {routingPolicyAnthropic(routingPolicyMid), routingPolicyAnthropic(routingPolicyLow)}}, session: demoted},
			first, routingPolicyRequest(next.body, demoted.editPins("default_mid", func(p *sessionpin.Pin) { p.DemotedModels = []string{routingPolicyMid} }))),
		routingPolicyScenario("band_swap_stay_serves_predicted_band_member", routingPolicySetup{turns: routingPolicyTurns(routingPolicyBandPair(routingPolicyLow, routingPolicyHigh)), options: bandSwap},
			first, next),
		routingPolicyScenario("band_swap_excluded_member_serves_anchor", routingPolicySetup{turns: routingPolicyTurns(routingPolicyBandPair(routingPolicyLow, routingPolicyHigh)), options: bandSwap},
			first, routingPolicyRequest(next.body, routingPolicyExcludeModels(routingPolicyHigh))),
	}
}

func routingPolicyEscalationAndClassifierScenarios() []goldenScenario {
	first, next := goldenRequest{body: routingPolicyFirstTurn}, goldenRequest{body: routingPolicyFollowUp("Now say bye")}
	served, excluded, providerNotEnabled := &routingPolicySession{}, &routingPolicySession{}, &routingPolicySession{}
	escalate := func(p *sessionpin.Pin) {
		p.Model, p.Provider, p.Reason = routingPolicyHigh, providers.ProviderAnthropic, translate.ReasonLoopEscalation
	}
	return []goldenScenario{
		routingPolicyScenario("loop_escalation_pin_served_and_refreshed", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow)), session: served},
			first, routingPolicyRequest(next.body, served.editPins("default_mid", escalate))),
		routingPolicyScenario("loop_escalation_pin_excluded_is_expired", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow)), session: excluded},
			first, routingPolicyRequest(next.body, excluded.editPins("default_mid", escalate), routingPolicyExcludeModels(routingPolicyHigh))),
		routingPolicyScenario("loop_escalation_pin_provider_not_enabled_dropped", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow)), session: providerNotEnabled,
			options: []func(*proxy.Service) *proxy.Service{excludeCompatProvider}},
			first, routingPolicyRequest(next.body, providerNotEnabled.editPins("default_mid", routingPolicyMoveToCompat(translate.ReasonLoopEscalation)))),
		routingPolicyScenario("classifier_strategy_without_thread_fails_closed", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow))},
			routingPolicyRefused(routingPolicyFirstTurn, nil, routingPolicyStrategy(router.StrategyLLMClassifier))),
		routingPolicyScenario("classifier_strategy_rejects_utility_turn", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow))},
			routingPolicyRefused(goldenClassifierBody, nil, routingPolicyStrategy(router.StrategyLLMClassifier))),
		routingPolicyScenario("strategy_without_router_fails_closed", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow))},
			routingPolicyRefused(routingPolicyFirstTurn, nil, routingPolicyStrategy(router.StrategyRL))),
		routingPolicyScenario("policy_pin_not_honoured_fails_closed", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow))},
			routingPolicyRefused(routingPolicyFirstTurn, nil, routingPolicyAuthorizedPin)),
		routingPolicyScenario("policy_pin_honoured_skips_session_pin", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow), router.Decision{
			Provider: providers.ProviderAnthropic, Model: routingPolicyHigh, Reason: "golden_route", Metadata: &router.RoutingMetadata{PolicyPinHonoured: true}})},
			first, routingPolicyRequest(next.body, routingPolicyAuthorizedPin)),
	}
}

// routingPolicySelectionGateScenarios cover HMM cost gating and the authoritative
// per-turn upgrade and downgrade guards.
func routingPolicySelectionGateScenarios() []goldenScenario {
	first, next := goldenRequest{body: routingPolicyFirstTurn}, goldenRequest{body: routingPolicyFollowUp("Now say bye")}
	authoritative := policy.Capabilities{AuthoritativePerTurnSelection: true}
	excluded := routingPolicyRequest(next.body, routingPolicyExcludeModels(routingPolicyLow))
	return []goldenScenario{
		routingPolicyScenario("hmm_first_turn_routes_unpinned", routingPolicySetup{turns: routingPolicyTurns(routingPolicyHMM(routingPolicyLow, 0.9))}, first),
		routingPolicyScenario("hmm_low_confidence_upgrade_stays", routingPolicySetup{turns: routingPolicyTurns(routingPolicyHMM(routingPolicyLow, 0.9), routingPolicyHMM(routingPolicyHigh, 0.3))}, first, next),
		routingPolicyScenario("hmm_confident_upgrade_switches", routingPolicySetup{turns: routingPolicyTurns(routingPolicyHMM(routingPolicyLow, 0.9), routingPolicyHMM(routingPolicyHigh, 0.9))}, first, next),
		routingPolicyScenario("hmm_cheaper_fresh_on_warm_history", routingPolicySetup{turns: routingPolicyTurns(routingPolicyHMM(routingPolicyHigh, 0.9), routingPolicyHMM(routingPolicyLow, 0.9))}, first, next),
		routingPolicyScenario("hmm_tool_execution_phase_change_switches", routingPolicySetup{turns: routingPolicyTurns(routingPolicyHMMReason(routingPolicyLow, "hmm_policy:tool_execution", 0.9), routingPolicyHMM(routingPolicyHigh, 0.3))},
			first, next),
		routingPolicyScenario("hmm_excluded_history_model_not_reused", routingPolicySetup{turns: routingPolicyTurns(routingPolicyHMM(routingPolicyLow, 0.9), routingPolicyHMM(routingPolicyHigh, 0.3))}, first, excluded),
		routingPolicyScenario("authoritative_first_turn_writes_pin", routingPolicySetup{turns: routingPolicyTurns(routingPolicyHMM(routingPolicyLow, 0.9)), caps: authoritative}, first),
		routingPolicyScenario("authoritative_low_confidence_upgrade_keeps_pin", routingPolicySetup{turns: routingPolicyTurns(routingPolicyHMM(routingPolicyLow, 0.9), routingPolicyHMM(routingPolicyHigh, 0.3)), caps: authoritative},
			first, next),
		routingPolicyScenario("authoritative_confident_upgrade_served", routingPolicySetup{turns: routingPolicyTurns(routingPolicyHMM(routingPolicyLow, 0.9), routingPolicyHMM(routingPolicyHigh, 0.9)), caps: authoritative},
			first, next),
		routingPolicyScenario("authoritative_upgrade_gate_off_serves_low_confidence", routingPolicySetup{turns: routingPolicyTurns(routingPolicyHMM(routingPolicyLow, 0.9), routingPolicyHMM(routingPolicyHigh, 0.3)), caps: authoritative,
			options: []func(*proxy.Service) *proxy.Service{func(s *proxy.Service) *proxy.Service { return s.WithAuthoritativeUpgradeGate(false) }}},
			first, next),
		routingPolicyScenario("authoritative_downgrade_vote_held_by_hysteresis", routingPolicySetup{turns: routingPolicyTurns(routingPolicyHMM(routingPolicyHigh, 0.9), routingPolicyHMM(routingPolicyLow, 0.9)), caps: authoritative},
			first, next),
		routingPolicyScenario("authoritative_downgrade_served_on_second_vote", routingPolicySetup{turns: routingPolicyTurns(routingPolicyHMM(routingPolicyHigh, 0.9), routingPolicyHMM(routingPolicyLow, 0.9)), caps: authoritative},
			first, next, goldenRequest{body: routingPolicyFollowUp("And once more")}),
		routingPolicyScenario("authoritative_low_confidence_downgrade_gated", routingPolicySetup{turns: routingPolicyTurns(routingPolicyHMM(routingPolicyHigh, 0.9), routingPolicyHMM(routingPolicyLow, 0.2)), caps: authoritative,
			options: []func(*proxy.Service) *proxy.Service{func(s *proxy.Service) *proxy.Service { return s.WithAuthoritativeDowngradeGate(true) }}},
			first, next),
	}
}

func routingPolicyForceModelScenarios() []goldenScenario {
	forced := goldenRequest{body: routingPolicyFirstTurn, headers: routingPolicyForce(routingPolicyHigh)}
	next := goldenRequest{body: routingPolicyFollowUp("Now say bye")}
	moved := &routingPolicySession{}
	return []goldenScenario{
		routingPolicyScenario("force_model_header_pins_session", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow))}, forced),
		routingPolicyScenario("force_model_persists_without_header", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow))}, forced, next),
		routingPolicyScenario("force_model_unknown_fails_closed", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow))},
			routingPolicyRefused(routingPolicyFirstTurn, routingPolicyForce("no-such-model"))),
		routingPolicyScenario("force_model_excluded_fails_closed", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow))},
			routingPolicyRefused(routingPolicyFirstTurn, routingPolicyForce(routingPolicyHigh), routingPolicyExcludeModels(routingPolicyHigh))),
		routingPolicyScenario("force_model_outside_allowlist_fails_closed", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow))},
			routingPolicyRefused(routingPolicyFirstTurn, routingPolicyForce(routingPolicyHigh), routingPolicyAllowModels(routingPolicyLow))),
		routingPolicyScenario("force_model_pin_on_excluded_provider_fails_closed", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow)), session: moved,
			options: []func(*proxy.Service) *proxy.Service{excludeCompatProvider}},
			forced, routingPolicyRefused(next.body, nil, moved.editPins("force_model", routingPolicyMoveToCompat(translate.ReasonUserForceModel)))),
		routingPolicyScenario("force_model_text_only_dropped_for_image_turn", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyHigh))},
			goldenRequest{body: routingPolicyFirstTurn, headers: routingPolicyForce(routingPolicyTextOnly)}, goldenRequest{body: routingPolicyImageTurn}),
		routingPolicyScenario("force_cluster_on_non_hmm_strategy_fails_closed", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow))},
			routingPolicyRefused(routingPolicyFirstTurn, map[string]string{proxy.ForceClusterHeader: "golden-group"}, routingPolicyStrategy(router.StrategyRL))),
		routingPolicyScenario("force_cluster_forwarded_to_policy", routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow)), trace: true},
			goldenRequest{body: routingPolicyFirstTurn, headers: map[string]string{proxy.ForceClusterHeader: "golden-group"}}),
	}
}

// routingPolicyEligibilityScenarios record the eligibility inputs the policy receives.
func routingPolicyEligibilityScenarios() []goldenScenario {
	first := goldenRequest{body: routingPolicyFirstTurn}
	traced := routingPolicySetup{turns: routingPolicyTurns(routingPolicyAnthropic(routingPolicyLow)), trace: true}
	tracedAvailable := traced
	tracedAvailable.options = []func(*proxy.Service) *proxy.Service{withAvailable}
	tracedProviderOverride := traced
	tracedProviderOverride.options = []func(*proxy.Service) *proxy.Service{func(s *proxy.Service) *proxy.Service {
		return s.WithExcludedProvidersOverride([]string{providers.ProviderFireworks})
	}}
	customKey := &auth.ExternalAPIKey{ID: "golden-byok", Provider: providers.ProviderOpenRouter, Plaintext: []byte("sk-golden"),
		BaseURL: "https://custom.invalid/v1", ModelAliases: map[string]string{routingPolicyLow: "golden/haiku"}}
	gatewayKey := &auth.ExternalAPIKey{ID: "golden-gateway", Provider: providers.ProviderAnthropicGateway, Plaintext: []byte("sk-golden"),
		BaseURL: "https://gateway.invalid", ModelAliases: map[string]string{routingPolicyLow: "gateway-haiku"}}
	gatewayUpstreams := routingPolicyUpstreams()
	gatewayUpstreams[providers.ProviderAnthropicGateway] = []goldenStep{anthropicOK(false, "ok")}
	return []goldenScenario{
		routingPolicyScenario("eligibility_deployment_keyed_providers", traced, first),
		routingPolicyScenario("eligibility_operator_excluded_providers_override", tracedProviderOverride, first),
		routingPolicyScenario("eligibility_installation_excluded_models", traced, routingPolicyRequest(routingPolicyFirstTurn, routingPolicyExcludeModels(routingPolicyHigh, routingPolicyTextOnly))),
		routingPolicyScenario("eligibility_allowlist_desugars_over_routable_universe", tracedAvailable, routingPolicyRequest(routingPolicyFirstTurn, routingPolicyAllowModels(routingPolicyLow, routingPolicyMid))),
		routingPolicyScenario("eligibility_preferred_models_forwarded", traced,
			routingPolicyRequest(routingPolicyFirstTurn, routingPolicyPreferModels(routingPolicyMid, routingPolicyLow))),
		routingPolicyScenario("eligibility_byok_alias_adds_custom_binding", traced,
			routingPolicyRequest(routingPolicyFirstTurn, routingPolicyExternalKeys(customKey))),
		{name: "routing_policy_eligibility_gateway_key_is_exclusive", ingress: ingressAnthropic, upstreams: gatewayUpstreams,
			configure: routingPolicySetup{turns: routingPolicyTurns(decision(providers.ProviderAnthropicGateway, routingPolicyLow)), trace: true}.configure,
			requests:  []goldenRequest{routingPolicyRequest(routingPolicyFirstTurn, routingPolicyExternalKeys(gatewayKey))}},
	}
}
