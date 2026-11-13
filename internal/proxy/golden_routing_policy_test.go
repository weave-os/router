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
	rpLow      = "claude-haiku-4-5"
	rpMid      = "claude-sonnet-4-5"
	rpHigh     = "claude-opus-4-7"
	rpTextOnly = "z-ai/glm-5.1"
)

var rpAvailableModels = map[string]struct{}{rpLow: {}, rpMid: {}, rpHigh: {}, rpTextOnly: {}, goldenCompatModel: {}}

func rpFollowUp(text string) string {
	return `{"model":"claude-sonnet-4-5","max_tokens":4096,"messages":[{"role":"user","content":"Say hi"},` +
		`{"role":"assistant","content":"hi"},{"role":"user","content":"` + text + `"}]}`
}

const rpToolResultTurn = `{"model":"claude-sonnet-4-5","max_tokens":4096,
"tools":[{"name":"Bash","description":"run","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],
"messages":[
{"role":"user","content":"Say hi"},
{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"echo hi"}}]},
{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"hi"}]}]}`

const rpImageTurn = `{"model":"claude-sonnet-4-5","max_tokens":4096,"messages":[{"role":"user","content":"Say hi"},
{"role":"assistant","content":"hi"},
{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}},{"type":"text","text":"What is this?"}]}]}`

var rpFirstTurn = goldenAnthropicBody(false)

func rpMaxedOut() goldenStep {
	return goldenOK("application/json", []string{`{"id":"msg_up","type":"message","role":"assistant","model":"` + goldenAnthropicModel +
		`","content":[{"type":"text","text":"truncated"}],"stop_reason":"max_tokens","usage":{"input_tokens":12,"output_tokens":4096}}`})
}

func rpUpstreams() map[string][]goldenStep {
	return map[string][]goldenStep{
		providers.ProviderAnthropic: {anthropicOK(false, "ok")},
		providers.ProviderFireworks: {compatOKWithModel(false, "accounts/fireworks/models/glm-5p1", "ok")},
	}
}

func rpAnthropic(model string) router.Decision { return decision(providers.ProviderAnthropic, model) }

// rpHMM is a non-authoritative HMM selection with a classifier confidence.
func rpHMM(model string, confidence float32) router.Decision {
	return rpHMMReason(model, "hmm_policy", confidence)
}

func rpHMMReason(model, reason string, confidence float32) router.Decision {
	return router.Decision{Provider: providers.ProviderAnthropic, Model: model, Reason: reason, Metadata: &router.RoutingMetadata{
		Strategy: string(router.StrategyHMMEmbedding), ChosenScore: confidence, PolicyGroup: "golden-group",
	}}
}

// rpBandPair is a scored decision that anchors a {model, paired} band.
func rpBandPair(model, paired string) router.Decision {
	d := rpAnthropic(model)
	d.Metadata = &router.RoutingMetadata{PairedModel: paired, PairedProvider: providers.ProviderAnthropic, Embedding: make([]float32, bandswap.EmbedDim)}
	return d
}

func rpTurns(turns ...router.Decision) [][]router.Decision {
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
		r.rec.routes[len(r.rec.routes)-1].Request = goldenValue(rpRequestTraceOf(req))
		r.rec.mu.Unlock()
	}
	return d, err
}

type rpRequestTrace struct {
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

func rpRequestTraceOf(req router.Request) rpRequestTrace {
	return rpRequestTrace{
		EnabledProviders:     rpSorted(req.EnabledProviders),
		AllowedModels:        rpSorted(req.AllowedModels),
		PreferredModels:      req.PreferredModels,
		CustomBindings:       req.CustomBindings,
		GatewayProviders:     rpSorted(req.GatewayProviders),
		SafetyExcludedModels: rpSorted(req.SafetyExcludedModels),
		PreviousPolicyGroup:  string(req.PreviousPolicyGroup),
		ForceCluster:         req.ForceCluster,
		PolicyTurnContext:    req.PolicyTurnContext,
	}
}

func rpSorted(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// rpSession lets a later request edit the session state an earlier one wrote.
type rpSession struct{ h *goldenHarness }

func (s *rpSession) editPins(role string, edit func(*sessionpin.Pin)) func(context.Context) context.Context {
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

func rpExpire(p *sessionpin.Pin) { p.PinnedUntil = time.Now().Add(-time.Minute) }

func rpEvict(p *sessionpin.Pin) { p.PinnedUntil = goldenEpoch.Add(-time.Minute) }

type rpSetup struct {
	turns   [][]router.Decision
	caps    policy.Capabilities
	trace   bool
	session *rpSession
	options []func(*proxy.Service) *proxy.Service
}

func (p rpSetup) configure(svc *proxy.Service, h *goldenHarness) *proxy.Service {
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

func rpScenario(name string, setup rpSetup, requests ...goldenRequest) goldenScenario {
	return goldenScenario{name: "routing_policy_" + name, ingress: ingressAnthropic, upstreams: rpUpstreams(),
		requests: requests, configure: setup.configure}
}

func rpCtx(decorators ...func(context.Context) context.Context) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		for _, d := range decorators {
			ctx = d(ctx)
		}
		return ctx
	}
}

func rpExcludeModels(models ...string) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		return context.WithValue(ctx, proxy.InstallationExcludedModelsContextKey{}, models)
	}
}

func rpAllowModels(models ...string) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		return context.WithValue(ctx, proxy.InstallationAllowedModelsContextKey{}, models)
	}
}

func rpPreferModels(models ...string) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		return context.WithValue(ctx, proxy.InstallationPreferredModelsContextKey{}, models)
	}
}

func rpExternalKeys(keys ...*auth.ExternalAPIKey) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		return context.WithValue(ctx, proxy.ExternalAPIKeysContextKey{}, keys)
	}
}

// excludeCompatProvider is the operator provider exclusion; a stored pin on
// that provider stands for one the installation has since stopped enabling.
func excludeCompatProvider(svc *proxy.Service) *proxy.Service {
	return svc.WithExcludedProvidersOverride([]string{providers.ProviderFireworks})
}

func rpMoveToCompat(reason string) func(*sessionpin.Pin) {
	return func(p *sessionpin.Pin) {
		p.Model, p.Provider, p.Reason = rpTextOnly, providers.ProviderFireworks, reason
	}
}

func rpStrategy(strategy router.Strategy) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context { return router.WithStrategy(ctx, strategy) }
}

func rpPolicyPin(ctx context.Context) context.Context {
	return router.WithPolicyPinRequest(ctx, router.PolicyPinRequest{Authorized: true, Pin: router.PolicyPin{
		ArtifactSHA256: strings.Repeat("a", 64), RosterSHA256: strings.Repeat("b", 64)}})
}

func rpRequest(body string, decorators ...func(context.Context) context.Context) goldenRequest {
	return goldenRequest{body: body, decorateContext: rpCtx(decorators...)}
}

func rpRefused(body string, headers map[string]string, decorators ...func(context.Context) context.Context) goldenRequest {
	return goldenRequest{body: body, headers: headers, decorateContext: rpCtx(decorators...), noTelemetry: true}
}

func rpForce(model string) map[string]string { return map[string]string{proxy.ForceModelHeader: model} }

func withAvailable(svc *proxy.Service) *proxy.Service {
	return svc.WithAvailableModels(rpAvailableModels)
}

func goldenRoutingPolicyScenarios() []goldenScenario {
	return append(append(append(append(rpSessionPinScenarios(), rpEscalationAndClassifierScenarios()...),
		rpSelectionGateScenarios()...), rpForceModelScenarios()...), rpEligibilityScenarios()...)
}

// rpSessionPinScenarios cover pin load, refresh, expiry and every guard that
// drops a stored pin before selection.
func rpSessionPinScenarios() []goldenScenario {
	first, next := goldenRequest{body: rpFirstTurn}, goldenRequest{body: rpFollowUp("Now say bye")}
	expired, evicted, mismatched := &rpSession{}, &rpSession{}, &rpSession{}
	disabled, demoted, unenabled := &rpSession{}, &rpSession{}, &rpSession{}
	bandSwap := []func(*proxy.Service) *proxy.Service{withAvailable, func(s *proxy.Service) *proxy.Service { return s.WithBandSwap(true) }}
	return []goldenScenario{
		rpScenario("first_turn_writes_session_pin", rpSetup{turns: rpTurns(rpAnthropic(rpLow))}, first),
		rpScenario("planner_disabled_keeps_first_decision", rpSetup{turns: rpTurns(rpAnthropic(rpLow), rpAnthropic(rpHigh)),
			options: []func(*proxy.Service) *proxy.Service{func(s *proxy.Service) *proxy.Service { return s.WithPlannerEnabled(false) }}},
			first, next),
		rpScenario("planner_same_model_refreshes_pin", rpSetup{turns: rpTurns(rpAnthropic(rpLow))}, first, next, next),
		rpScenario("planner_tier_upgrade_switches", rpSetup{turns: rpTurns(rpAnthropic(rpLow), rpAnthropic(rpHigh))}, first, next),
		rpScenario("planner_tier_downgrade_on_warm_pin", rpSetup{turns: rpTurns(rpAnthropic(rpHigh), rpAnthropic(rpLow))}, first, next),
		rpScenario("expired_pin_reanchors_prior_model", rpSetup{turns: rpTurns(rpAnthropic(rpHigh), rpAnthropic(rpLow)),
			session: expired, options: []func(*proxy.Service) *proxy.Service{withAvailable}},
			first, rpRequest(next.body, expired.editPins("default_mid", rpExpire))),
		rpScenario("pin_absent_from_store_routes_as_first_turn", rpSetup{turns: rpTurns(rpAnthropic(rpHigh), rpAnthropic(rpLow)),
			session: evicted, options: []func(*proxy.Service) *proxy.Service{withAvailable}},
			first, rpRequest(next.body, evicted.editPins("default_mid", rpEvict))),
		rpScenario("pin_from_other_strategy_ignored", rpSetup{turns: rpTurns(rpAnthropic(rpHigh), rpAnthropic(rpLow)), session: mismatched},
			first, rpRequest(next.body, mismatched.editPins("default_mid", func(p *sessionpin.Pin) { p.Strategy = router.StrategyHMMBeta }))),
		rpScenario("pin_on_excluded_provider_falls_through", rpSetup{turns: rpTurns(rpAnthropic(rpLow)), trace: true, session: unenabled,
			options: []func(*proxy.Service) *proxy.Service{excludeCompatProvider}},
			first, rpRequest(next.body, unenabled.editPins("default_mid", rpMoveToCompat("golden_route")))),
		rpScenario("pin_model_excluded_falls_through", rpSetup{turns: [][]router.Decision{{rpAnthropic(rpLow)}, {rpAnthropic(rpLow), rpAnthropic(rpMid)}}},
			first, rpRequest(next.body, rpExcludeModels(rpLow))),
		rpScenario("pin_outside_installation_allowlist_falls_through", rpSetup{turns: [][]router.Decision{{rpAnthropic(rpLow)}, {rpAnthropic(rpLow), rpAnthropic(rpMid)}},
			options: []func(*proxy.Service) *proxy.Service{withAvailable}},
			first, rpRequest(next.body, rpAllowModels(rpMid, rpHigh))),
		rpScenario("text_only_pin_dropped_for_image_turn", rpSetup{turns: rpTurns(decision(providers.ProviderFireworks, rpTextOnly), rpAnthropic(rpHigh))},
			first, goldenRequest{body: rpImageTurn}),
		{name: "routing_policy_maxed_out_model_excluded_next_turn", ingress: ingressAnthropic,
			upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {rpMaxedOut(), anthropicOK(false, "ok")}},
			configure: rpSetup{turns: [][]router.Decision{{rpAnthropic(rpLow)}, {rpAnthropic(rpLow), rpAnthropic(rpMid)}}}.configure,
			requests:  []goldenRequest{first, {body: rpFollowUp("continue")}}},
		rpScenario("tool_result_reuses_pin_when_scoring_off", rpSetup{turns: rpTurns(rpAnthropic(rpLow), rpAnthropic(rpHigh)),
			options: []func(*proxy.Service) *proxy.Service{func(s *proxy.Service) *proxy.Service { return s.WithScoreToolResultTurns(false) }}},
			first, goldenRequest{body: rpToolResultTurn}),
		rpScenario("tool_result_rescored_by_default", rpSetup{turns: rpTurns(rpAnthropic(rpLow), rpAnthropic(rpHigh))},
			first, goldenRequest{body: rpToolResultTurn}),
		rpScenario("session_disabled_provider_leaves_enabled_set", rpSetup{turns: rpTurns(rpAnthropic(rpLow)), trace: true, session: disabled},
			first, rpRequest(next.body, disabled.editPins("default_mid", func(p *sessionpin.Pin) { p.DisabledProviders = []string{providers.ProviderFireworks} }))),
		rpScenario("session_demoted_model_automatically_excluded", rpSetup{turns: [][]router.Decision{{rpAnthropic(rpLow)}, {rpAnthropic(rpMid), rpAnthropic(rpLow)}}, session: demoted},
			first, rpRequest(next.body, demoted.editPins("default_mid", func(p *sessionpin.Pin) { p.DemotedModels = []string{rpMid} }))),
		rpScenario("band_swap_stay_serves_predicted_band_member", rpSetup{turns: rpTurns(rpBandPair(rpLow, rpHigh)), options: bandSwap},
			first, next),
		rpScenario("band_swap_excluded_member_serves_anchor", rpSetup{turns: rpTurns(rpBandPair(rpLow, rpHigh)), options: bandSwap},
			first, rpRequest(next.body, rpExcludeModels(rpHigh))),
	}
}

func rpEscalationAndClassifierScenarios() []goldenScenario {
	first, next := goldenRequest{body: rpFirstTurn}, goldenRequest{body: rpFollowUp("Now say bye")}
	served, excluded, unenabled := &rpSession{}, &rpSession{}, &rpSession{}
	escalate := func(p *sessionpin.Pin) {
		p.Model, p.Provider, p.Reason = rpHigh, providers.ProviderAnthropic, translate.ReasonLoopEscalation
	}
	return []goldenScenario{
		rpScenario("loop_escalation_pin_served_and_refreshed", rpSetup{turns: rpTurns(rpAnthropic(rpLow)), session: served},
			first, rpRequest(next.body, served.editPins("default_mid", escalate))),
		rpScenario("loop_escalation_pin_excluded_is_expired", rpSetup{turns: rpTurns(rpAnthropic(rpLow)), session: excluded},
			first, rpRequest(next.body, excluded.editPins("default_mid", escalate), rpExcludeModels(rpHigh))),
		rpScenario("loop_escalation_pin_provider_not_enabled_dropped", rpSetup{turns: rpTurns(rpAnthropic(rpLow)), session: unenabled,
			options: []func(*proxy.Service) *proxy.Service{excludeCompatProvider}},
			first, rpRequest(next.body, unenabled.editPins("default_mid", rpMoveToCompat(translate.ReasonLoopEscalation)))),
		rpScenario("classifier_strategy_without_thread_fails_closed", rpSetup{turns: rpTurns(rpAnthropic(rpLow))},
			rpRefused(rpFirstTurn, nil, rpStrategy(router.StrategyLLMClassifier))),
		rpScenario("classifier_strategy_rejects_utility_turn", rpSetup{turns: rpTurns(rpAnthropic(rpLow))},
			rpRefused(goldenClassifierBody, nil, rpStrategy(router.StrategyLLMClassifier))),
		rpScenario("strategy_without_router_fails_closed", rpSetup{turns: rpTurns(rpAnthropic(rpLow))},
			rpRefused(rpFirstTurn, nil, rpStrategy(router.StrategyRL))),
		rpScenario("policy_pin_not_honoured_fails_closed", rpSetup{turns: rpTurns(rpAnthropic(rpLow))},
			rpRefused(rpFirstTurn, nil, rpPolicyPin)),
		rpScenario("policy_pin_honoured_skips_session_pin", rpSetup{turns: rpTurns(rpAnthropic(rpLow), router.Decision{
			Provider: providers.ProviderAnthropic, Model: rpHigh, Reason: "golden_route", Metadata: &router.RoutingMetadata{PolicyPinHonoured: true}})},
			first, rpRequest(next.body, rpPolicyPin)),
	}
}

// rpSelectionGateScenarios cover HMM cost gating and the authoritative
// per-turn upgrade and downgrade guards.
func rpSelectionGateScenarios() []goldenScenario {
	first, next := goldenRequest{body: rpFirstTurn}, goldenRequest{body: rpFollowUp("Now say bye")}
	authoritative := policy.Capabilities{AuthoritativePerTurnSelection: true}
	excluded := rpRequest(next.body, rpExcludeModels(rpLow))
	return []goldenScenario{
		rpScenario("hmm_first_turn_routes_unpinned", rpSetup{turns: rpTurns(rpHMM(rpLow, 0.9))}, first),
		rpScenario("hmm_low_confidence_upgrade_stays", rpSetup{turns: rpTurns(rpHMM(rpLow, 0.9), rpHMM(rpHigh, 0.3))}, first, next),
		rpScenario("hmm_confident_upgrade_switches", rpSetup{turns: rpTurns(rpHMM(rpLow, 0.9), rpHMM(rpHigh, 0.9))}, first, next),
		rpScenario("hmm_cheaper_fresh_on_warm_history", rpSetup{turns: rpTurns(rpHMM(rpHigh, 0.9), rpHMM(rpLow, 0.9))}, first, next),
		rpScenario("hmm_tool_execution_phase_change_switches", rpSetup{turns: rpTurns(rpHMMReason(rpLow, "hmm_policy:tool_execution", 0.9), rpHMM(rpHigh, 0.3))},
			first, next),
		rpScenario("hmm_excluded_history_model_not_reused", rpSetup{turns: rpTurns(rpHMM(rpLow, 0.9), rpHMM(rpHigh, 0.3))}, first, excluded),
		rpScenario("authoritative_first_turn_writes_pin", rpSetup{turns: rpTurns(rpHMM(rpLow, 0.9)), caps: authoritative}, first),
		rpScenario("authoritative_low_confidence_upgrade_keeps_pin", rpSetup{turns: rpTurns(rpHMM(rpLow, 0.9), rpHMM(rpHigh, 0.3)), caps: authoritative},
			first, next),
		rpScenario("authoritative_confident_upgrade_served", rpSetup{turns: rpTurns(rpHMM(rpLow, 0.9), rpHMM(rpHigh, 0.9)), caps: authoritative},
			first, next),
		rpScenario("authoritative_upgrade_gate_off_serves_low_confidence", rpSetup{turns: rpTurns(rpHMM(rpLow, 0.9), rpHMM(rpHigh, 0.3)), caps: authoritative,
			options: []func(*proxy.Service) *proxy.Service{func(s *proxy.Service) *proxy.Service { return s.WithAuthoritativeUpgradeGate(false) }}},
			first, next),
		rpScenario("authoritative_downgrade_vote_held_by_hysteresis", rpSetup{turns: rpTurns(rpHMM(rpHigh, 0.9), rpHMM(rpLow, 0.9)), caps: authoritative},
			first, next),
		rpScenario("authoritative_downgrade_served_on_second_vote", rpSetup{turns: rpTurns(rpHMM(rpHigh, 0.9), rpHMM(rpLow, 0.9)), caps: authoritative},
			first, next, goldenRequest{body: rpFollowUp("And once more")}),
		rpScenario("authoritative_low_confidence_downgrade_gated", rpSetup{turns: rpTurns(rpHMM(rpHigh, 0.9), rpHMM(rpLow, 0.2)), caps: authoritative,
			options: []func(*proxy.Service) *proxy.Service{func(s *proxy.Service) *proxy.Service { return s.WithAuthoritativeDowngradeGate(true) }}},
			first, next),
	}
}

func rpForceModelScenarios() []goldenScenario {
	forced := goldenRequest{body: rpFirstTurn, headers: rpForce(rpHigh)}
	next := goldenRequest{body: rpFollowUp("Now say bye")}
	moved := &rpSession{}
	return []goldenScenario{
		rpScenario("force_model_header_pins_session", rpSetup{turns: rpTurns(rpAnthropic(rpLow))}, forced),
		rpScenario("force_model_persists_without_header", rpSetup{turns: rpTurns(rpAnthropic(rpLow))}, forced, next),
		rpScenario("force_model_unknown_fails_closed", rpSetup{turns: rpTurns(rpAnthropic(rpLow))},
			rpRefused(rpFirstTurn, rpForce("no-such-model"))),
		rpScenario("force_model_excluded_fails_closed", rpSetup{turns: rpTurns(rpAnthropic(rpLow))},
			rpRefused(rpFirstTurn, rpForce(rpHigh), rpExcludeModels(rpHigh))),
		rpScenario("force_model_outside_allowlist_fails_closed", rpSetup{turns: rpTurns(rpAnthropic(rpLow))},
			rpRefused(rpFirstTurn, rpForce(rpHigh), rpAllowModels(rpLow))),
		rpScenario("force_model_pin_on_excluded_provider_fails_closed", rpSetup{turns: rpTurns(rpAnthropic(rpLow)), session: moved,
			options: []func(*proxy.Service) *proxy.Service{excludeCompatProvider}},
			forced, rpRefused(next.body, nil, moved.editPins("force_model", rpMoveToCompat(translate.ReasonUserForceModel)))),
		rpScenario("force_model_text_only_dropped_for_image_turn", rpSetup{turns: rpTurns(rpAnthropic(rpHigh))},
			goldenRequest{body: rpFirstTurn, headers: rpForce(rpTextOnly)}, goldenRequest{body: rpImageTurn}),
		rpScenario("force_cluster_on_non_hmm_strategy_fails_closed", rpSetup{turns: rpTurns(rpAnthropic(rpLow))},
			rpRefused(rpFirstTurn, map[string]string{proxy.ForceClusterHeader: "golden-group"}, rpStrategy(router.StrategyRL))),
		rpScenario("force_cluster_forwarded_to_policy", rpSetup{turns: rpTurns(rpAnthropic(rpLow)), trace: true},
			goldenRequest{body: rpFirstTurn, headers: map[string]string{proxy.ForceClusterHeader: "golden-group"}}),
	}
}

// rpEligibilityScenarios record the eligibility inputs the policy receives.
func rpEligibilityScenarios() []goldenScenario {
	first := goldenRequest{body: rpFirstTurn}
	traced := rpSetup{turns: rpTurns(rpAnthropic(rpLow)), trace: true}
	tracedAvailable := traced
	tracedAvailable.options = []func(*proxy.Service) *proxy.Service{withAvailable}
	tracedProviderOverride := traced
	tracedProviderOverride.options = []func(*proxy.Service) *proxy.Service{func(s *proxy.Service) *proxy.Service {
		return s.WithExcludedProvidersOverride([]string{providers.ProviderFireworks})
	}}
	customKey := &auth.ExternalAPIKey{ID: "golden-byok", Provider: providers.ProviderOpenRouter, Plaintext: []byte("sk-golden"),
		BaseURL: "https://custom.invalid/v1", ModelAliases: map[string]string{rpLow: "golden/haiku"}}
	gatewayKey := &auth.ExternalAPIKey{ID: "golden-gateway", Provider: providers.ProviderAnthropicGateway, Plaintext: []byte("sk-golden"),
		BaseURL: "https://gateway.invalid", ModelAliases: map[string]string{rpLow: "gateway-haiku"}}
	gatewayUpstreams := rpUpstreams()
	gatewayUpstreams[providers.ProviderAnthropicGateway] = []goldenStep{anthropicOK(false, "ok")}
	return []goldenScenario{
		rpScenario("eligibility_deployment_keyed_providers", traced, first),
		rpScenario("eligibility_operator_excluded_providers_override", tracedProviderOverride, first),
		rpScenario("eligibility_installation_excluded_models", traced, rpRequest(rpFirstTurn, rpExcludeModels(rpHigh, rpTextOnly))),
		rpScenario("eligibility_allowlist_desugars_over_routable_universe", tracedAvailable, rpRequest(rpFirstTurn, rpAllowModels(rpLow, rpMid))),
		rpScenario("eligibility_preferred_models_forwarded", traced,
			rpRequest(rpFirstTurn, rpPreferModels(rpMid, rpLow))),
		rpScenario("eligibility_byok_alias_adds_custom_binding", traced,
			rpRequest(rpFirstTurn, rpExternalKeys(customKey))),
		{name: "routing_policy_eligibility_gateway_key_is_exclusive", ingress: ingressAnthropic, upstreams: gatewayUpstreams,
			configure: rpSetup{turns: rpTurns(decision(providers.ProviderAnthropicGateway, rpLow)), trace: true}.configure,
			requests:  []goldenRequest{rpRequest(rpFirstTurn, rpExternalKeys(gatewayKey))}},
	}
}
