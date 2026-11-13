package proxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionstrategy"
	"weave-os/router/internal/translate"
)

const goldenConvPrefix = ingressAnthropic + "_conversation_"

// goldenConversationScenarios pin Conversation Lifecycle behavior: slash
// commands, post-command continuation, compaction and handover, loop and
// repetition detection, and tool-result turns.
func goldenConversationScenarios() []goldenScenario {
	var out []goldenScenario
	out = append(out, goldenCommandScenarios()...)
	out = append(out, goldenContinuationScenarios()...)
	out = append(out, goldenHandoverScenarios()...)
	out = append(out, goldenDetectionScenarios()...)
	return append(out, goldenToolResultScenarios()...)
}

func convUser(text string) string {
	return fmt.Sprintf(`{"role":"user","content":%s}`, convJSON(text))
}

func convAssistant(text string) string {
	return fmt.Sprintf(`{"role":"assistant","content":%s}`, convJSON(text))
}

func convToolUse(id, name, input string) string {
	return fmt.Sprintf(`{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":%q,"input":%s}]}`, id, name, input)
}

func convToolResult(id, content string) string {
	return fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":%s}]}`, id, convJSON(content))
}

func convJSON(v string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

const goldenConvTools = `[{"name":"Bash","description":"run","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}},` +
	`{"name":"Read","description":"read","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}},` +
	`{"name":"Edit","description":"edit","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]`

func convBody(stream bool, messages ...string) string {
	return fmt.Sprintf(`{"model":%q,"stream":%t,"max_tokens":1024,"tools":%s,"messages":[%s]}`,
		goldenAnthropicModel, stream, goldenConvTools, strings.Join(messages, ","))
}

func convOpenAIBody(messages ...string) string {
	return fmt.Sprintf(`{"model":%q,"stream":false,"messages":[%s]}`, goldenAnthropicModel, strings.Join(messages, ","))
}

func convOpenAIUser(text string) string {
	return fmt.Sprintf(`{"role":"user","content":%s}`, convJSON(text))
}

func convOpenAIAssistant(text string) string {
	return fmt.Sprintf(`{"role":"assistant","content":%s}`, convJSON(text))
}

const goldenConvOpening = "refactor the parser package"

func goldenSessionCtx(ctx context.Context) context.Context {
	return requestcontext.WithClientIdentity(ctx, proxy.ClientIdentity{ClientApp: proxy.ClientAppClaudeCode, SessionID: "golden-session"})
}

func inSession(req goldenRequest) goldenRequest {
	req.decorateContext = goldenSessionCtx
	return req
}

func anthropicSteps(n int) []goldenStep {
	steps := make([]goldenStep, n)
	for i := range steps {
		steps[i] = anthropicOK(false, fmt.Sprintf("reply %d", i+1))
	}
	return steps
}

func convScenario(name string, requests ...goldenRequest) goldenScenario {
	return goldenScenario{
		name: goldenConvPrefix + name, ingress: ingressAnthropic,
		decisions: []router.Decision{decision(providers.ProviderAnthropic, goldenAnthropicModel)},
		upstreams: map[string][]goldenStep{providers.ProviderAnthropic: anthropicSteps(len(requests))},
		requests:  requests,
	}
}

func convReq(messages ...string) goldenRequest {
	return goldenRequest{body: convBody(false, messages...)}
}

// convCmd is a turn answered by the router itself, which emits no request row.
func convCmd(messages ...string) goldenRequest {
	return goldenRequest{body: convBody(false, messages...), noTelemetry: true}
}

func withConvStores(sc goldenScenario, seed bool) goldenScenario {
	sc.configure = func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
		store := &goldenConversationStore{rec: h.rec, enabled: seed}
		return svc.WithRouterFeedbackStore(store).
			WithSessionStrategyStore(store).
			WithLoopEscalationStore(store).
			WithSpiralShadowStore(store).
			WithStruggleShadowStore(store)
	}
	return sc
}

func goldenCommandScenarios() []goldenScenario {
	first := convUser(goldenConvOpening)
	reply := convAssistant("Here is the plan for the parser package refactor.")
	forced := convScenario("force_model_command_then_forced_turn",
		convCmd(first, reply, convUser("/force-model "+goldenSiblingModel)),
		convReq(first, reply, convUser("/force-model "+goldenSiblingModel), convAssistant("forced"), convUser("continue")))
	forced.upstreams[providers.ProviderAnthropic] = []goldenStep{anthropicOK(false, "forced reply")}
	forcedStream := convScenario("force_model_command_stream_ack",
		goldenRequest{body: convBody(true, first, reply, convUser("/force-model "+goldenSiblingModel)), noTelemetry: true})
	unforce := convScenario("unforce_model_command_returns_to_routing",
		convCmd(first, reply, convUser("/force-model "+goldenSiblingModel)),
		convCmd(first, reply, convUser("/unforce-model")),
		convReq(first, reply, convUser("/unforce-model"), convAssistant("unforced"), convUser("continue")))
	unknown := convScenario("force_model_command_unknown_model", convCmd(first, reply, convUser("/force-model not-a-real-model")))
	openAIForce := goldenScenario{
		name: ingressOpenAI + "_conversation_force_model_command_ack", ingress: ingressOpenAI,
		decisions: []router.Decision{decision(providers.ProviderAnthropic, goldenAnthropicModel)},
		upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(false, "never")}},
		requests: []goldenRequest{{body: convOpenAIBody(convOpenAIUser(goldenConvOpening),
			convOpenAIAssistant("plan"), convOpenAIUser("/force-model "+goldenSiblingModel)), noTelemetry: true}},
	}
	session := convScenario("router_session_command", convCmd(first, reply, convUser("/router-session")))
	sessionWithID := convScenario("router_session_command_with_client_session", inSession(convCmd(first, reply, convUser("/router-session"))))
	models := convScenario("router_models_command", convCmd(first, reply, convUser("/router-models")))
	feedback := withConvStores(convScenario("router_feedback_command_records_event",
		convCmd(first, reply, convUser("/router-feedback the parser plan was a great pick"))), false)
	feedbackEmpty := withConvStores(convScenario("router_feedback_command_without_text",
		convCmd(first, reply, convUser("/router-feedback"))), false)
	openAIFeedback := withConvStores(goldenScenario{
		name: ingressOpenAI + "_conversation_router_feedback_command", ingress: ingressOpenAI,
		decisions: []router.Decision{decision(providers.ProviderAnthropic, goldenAnthropicModel)},
		upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(false, "never")}},
		requests: []goldenRequest{{body: convOpenAIBody(convOpenAIUser(goldenConvOpening),
			convOpenAIAssistant("plan"), convOpenAIUser("/router-feedback wrong model for this task")), noTelemetry: true}},
	}, false)
	betaNoStore := convScenario("beta_command_without_store", convCmd(first, reply, convUser("/beta")))
	betaDisable := withConvStores(convScenario("beta_command_disables_unavailable_strategy",
		inSession(convCmd(first, reply, convUser("/beta")))), true)
	historical := convScenario("historical_command_stripped_from_upstream",
		convReq(first, reply, convUser("/router-session"), convAssistant("session details"), convUser("now add tests")))
	return []goldenScenario{forced, forcedStream, unforce, unknown, openAIForce, session, sessionWithID, models,
		feedback, feedbackEmpty, openAIFeedback, betaNoStore, betaDisable, historical}
}

func goldenContinuationScenarios() []goldenScenario {
	first := convUser(goldenConvOpening)
	reply := convAssistant("Here is the plan for the parser package refactor.")
	grant := convScenario("post_command_continuation_granted_and_consumed",
		convReq(first),
		convCmd(first, reply, convUser("/router-session")),
		convReq(first, reply, convUser("/router-session"), convAssistant("session details"), convUser("continue the refactor")))
	noPin := convScenario("post_command_continuation_skipped_without_pin",
		convCmd(first, reply, convUser("/router-session")),
		convReq(first, reply, convUser("/router-session"), convAssistant("session details"), convUser("continue the refactor")))
	forced := convScenario("post_command_continuation_skipped_when_forced",
		convCmd(first, reply, convUser("/force-model "+goldenSiblingModel)),
		convCmd(first, reply, convUser("/router-models")),
		convReq(first, reply, convUser("/router-models"), convAssistant("models"), convUser("continue the refactor")))
	expired := convScenario("post_command_continuation_expires",
		convReq(first),
		convCmd(first, reply, convUser("/router-session")),
		goldenRequest{advance: 2 * time.Hour, body: convBody(false, first, reply, convUser("/router-session"),
			convAssistant("session details"), convUser("continue the refactor"))})
	return []goldenScenario{grant, noPin, forced, expired}
}

func convLongHistory(turns int) []string {
	msgs := []string{convUser(goldenConvOpening)}
	for i := range turns {
		msgs = append(msgs, convAssistant(fmt.Sprintf("step %d done", i)), convUser(fmt.Sprintf("next step %d", i)))
	}
	return msgs
}

func handoverScenario(name string, summarizer handover.Summarizer) goldenScenario {
	long := convLongHistory(6)
	short := []string{convUser(goldenConvOpening), convAssistant("Summary: parser split into lexer and grammar."), convUser("keep going")}
	sc := goldenScenario{
		name: goldenConvPrefix + name, ingress: ingressAnthropic,
		decisions: []router.Decision{decision(providers.ProviderFireworks, goldenCompatModel)},
		upstreams: map[string][]goldenStep{
			providers.ProviderFireworks: {compatOK(false, "first"), compatOK(false, "second")},
			providers.ProviderAnthropic: {anthropicOK(false, "never")},
		},
		requests: []goldenRequest{
			{body: convBody(false, long...), decorateContext: goldenSessionCtx},
			{body: convBody(false, short...), decorateContext: goldenSessionCtx},
		},
	}
	if summarizer != nil {
		sc.configure = func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
			if s, ok := summarizer.(*goldenSummarizer); ok {
				s.rec = h.rec
			}
			return svc.WithCompactionHandoverSummarizer(summarizer)
		}
	}
	return sc
}

const goldenHandoffSecret = "golden-handoff-secret-0123456789abcdef"

func handoffScenario(name string, requests ...goldenRequest) goldenScenario {
	sc := convScenario(name, requests...)
	sc.decisions = nil
	sc.configure = func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
		classifier := &goldenRouter{rec: h.rec, decisions: []router.Decision{decision(providers.ProviderAnthropic, goldenAnthropicModel)}}
		return svc.WithPiHandoffSecret(goldenHandoffSecret).
			WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: classifier,
				Capabilities: policy.Capabilities{AuthoritativePerTurnSelection: true}})
	}
	return sc
}

func handoffCtx(prepare bool) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		ctx = router.WithStrategy(ctx, router.StrategyHMM)
		if prepare {
			ctx = proxy.WithHandoffPreparation(ctx)
		}
		return ctx
	}
}

func withHandoffToken(body string, previous []byte) string {
	var prepared struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(previous, &prepared)
	return strings.Replace(body, `{"model"`, fmt.Sprintf(`{"weave_handoff":%q,"model"`, prepared.Token), 1)
}

func goldenHandoverScenarios() []goldenScenario {
	body := fmt.Sprintf(`{"model":%q,"max_tokens":1024,"stream":false,"metadata":{"user_id":"pi:golden"},"messages":[%s]}`,
		goldenAnthropicModel, convUser(goldenConvOpening))
	return []goldenScenario{
		handoverScenario("compaction_handover_summarizes_history", &goldenSummarizer{summary: "Earlier: parser split into lexer and grammar."}),
		handoverScenario("compaction_handover_summarizer_error_keeps_history", &goldenSummarizer{err: errors.New("summarizer unavailable")}),
		handoverScenario("compaction_handover_empty_summary_keeps_history", &goldenSummarizer{}),
		handoverScenario("compaction_handover_without_summarizer", nil),
		handoffScenario("handoff_prepare_then_resume_skips_reclassify",
			goldenRequest{body: body, decorateContext: handoffCtx(true), noTelemetry: true},
			goldenRequest{decorateContext: handoffCtx(false), bodyFrom: func(prev []byte) string { return withHandoffToken(body, prev) }}),
		handoffScenario("handoff_invalid_token_rejected",
			goldenRequest{body: strings.Replace(body, `{"model"`, `{"weave_handoff":"not-a-token","model"`, 1),
				decorateContext: handoffCtx(false), noTelemetry: true}),
		handoffScenario("handoff_prepare_bypassed_for_forced_model",
			goldenRequest{body: strings.Replace(body, convUser(goldenConvOpening), strings.Join([]string{convUser(goldenConvOpening), convAssistant("plan"), convUser("/force-model " + goldenSiblingModel)}, ","), 1),
				decorateContext: handoffCtx(true), noTelemetry: true}),
	}
}

func convRepeatedToolCalls(n int, name, input string) []string {
	msgs := []string{convUser(goldenConvOpening)}
	for i := range n {
		id := fmt.Sprintf("toolu_%02d", i)
		msgs = append(msgs, convToolUse(id, name, input), convToolResult(id, "same output"))
	}
	return msgs
}

func goldenDetectionScenarios() []goldenScenario {
	loop := withConvStores(convScenario("cyclic_tool_loop_escalates",
		convReq(convRepeatedToolCalls(24, "Read", `{"path":"parser.go"}`)...)), false)
	loopDisabled := withConvStores(convScenario("cyclic_tool_loop_disabled_records_event",
		convReq(convRepeatedToolCalls(24, "Read", `{"path":"parser.go"}`)...)), false)
	base := loopDisabled.configure
	loopDisabled.configure = func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
		return base(svc, h).WithLoopEscalationConfig(false, 0)
	}
	withEdit := convRepeatedToolCalls(23, "Read", `{"path":"parser.go"}`)
	withEdit = append(withEdit, convToolUse("toolu_edit", "Edit", `{"path":"parser.go"}`), convToolResult("toolu_edit", "ok"))
	editProgress := withConvStores(convScenario("cyclic_tool_loop_with_edit_is_progress", convReq(withEdit...)), false)

	stuck := convRepeatedToolCalls(1, "Bash", `{"command":"go test ./..."}`)
	noProgressReqs := make([]goldenRequest, 5)
	for i := range noProgressReqs {
		noProgressReqs[i] = convReq(stuck...)
	}
	noProgressReqs[4].noTelemetry = true
	noProgress := convScenario("no_progress_break_after_identical_tool_turns", noProgressReqs...)

	repeated := "I will now run the parser tests again to confirm the fix works as expected."
	repetition := []string{convUser(goldenConvOpening)}
	for i := range 3 {
		id := fmt.Sprintf("toolu_r%d", i)
		repetition = append(repetition,
			fmt.Sprintf(`{"role":"assistant","content":[{"type":"text","text":%s},{"type":"tool_use","id":%q,"name":"Bash","input":{"command":"go test ./parser/%d"}}]}`, convJSON(repeated), id, i),
			convToolResult(id, fmt.Sprintf("ok %d", i)))
	}
	repBreak := convReq(repetition...)
	repBreak.noTelemetry = true
	textRep := convScenario("text_repetition_break", repBreak)
	textRepOff := convScenario("text_repetition_break_disabled", convReq(repetition...))
	textRepOff.configure = func(svc *proxy.Service, _ *goldenHarness) *proxy.Service { return svc.WithTextRepetitionBreak(false) }
	openAIRep := []string{convOpenAIUser(goldenConvOpening)}
	for i := range 3 {
		id := fmt.Sprintf("call_r%d", i)
		openAIRep = append(openAIRep,
			fmt.Sprintf(`{"role":"assistant","content":%s,"tool_calls":[{"id":%q,"type":"function","function":{"name":"Bash","arguments":"{\"command\":\"go test ./parser/%d\"}"}}]}`, convJSON(repeated), id, i),
			fmt.Sprintf(`{"role":"tool","tool_call_id":%q,"content":"ok %d"}`, id, i))
	}
	openAITextRep := goldenScenario{
		name: ingressOpenAI + "_conversation_text_repetition_break", ingress: ingressOpenAI,
		decisions: []router.Decision{decision(providers.ProviderAnthropic, goldenAnthropicModel)},
		upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(false, "never")}},
		requests: []goldenRequest{{body: convOpenAIBody(openAIRep...), noTelemetry: true, decorateContext: func(ctx context.Context) context.Context {
			return requestcontext.WithClientIdentity(ctx, proxy.ClientIdentity{ClientApp: proxy.ClientAppOpencode})
		}}},
	}

	errs := []string{convUser(goldenConvOpening)}
	for i := range 12 {
		id := fmt.Sprintf("toolu_e%02d", i)
		errs = append(errs, convToolUse(id, "Bash", fmt.Sprintf(`{"command":"go build ./pkg%d"}`, i)),
			fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"is_error":true,"content":"build failed"}]}`, id))
	}
	consented := goldenRequest{body: convBody(false, errs...), decorateContext: func(ctx context.Context) context.Context {
		return context.WithValue(ctx, proxy.PolicyTrainingAllowedContextKey{}, true)
	}}
	spiral := withConvStores(convScenario("spiral_shadow_records_error_streak", consented), false)
	spiral.configure = chainConfigure(spiral.configure, func(svc *proxy.Service) *proxy.Service {
		return svc.WithSpiralShadowConfig(true).WithContentCapture(proxy.CaptureHashed, 0, nil)
	})
	spiralNoConsent := withConvStores(convScenario("spiral_shadow_skipped_without_training_consent", convReq(errs...)), false)

	var struggleHarness *goldenHarness
	agedPin := func(ctx context.Context) context.Context {
		// The detector reads pin age against the wall clock, not the fake clock.
		struggleHarness.rec.mu.Lock()
		defer struggleHarness.rec.mu.Unlock()
		for k, p := range struggleHarness.pins.pins {
			p.TurnCount, p.FirstPinnedAt = 80, time.Now().Add(-time.Hour)
			struggleHarness.pins.pins[k] = p
		}
		return ctx
	}
	struggle := withConvStores(convScenario("struggle_shadow_records_long_session",
		convReq(convLongHistory(1)...),
		goldenRequest{body: convBody(false, convLongHistory(2)...), decorateContext: agedPin}), false)
	struggle.configure = chainConfigure(struggle.configure, func(svc *proxy.Service) *proxy.Service { return svc.WithStruggleShadowConfig(true) })
	struggleBase := struggle.configure
	struggle.configure = func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
		struggleHarness = h
		return struggleBase(svc, h)
	}

	return []goldenScenario{loop, loopDisabled, editProgress, noProgress, textRep, textRepOff, openAITextRep, spiral, spiralNoConsent, struggle}
}

func chainConfigure(base func(*proxy.Service, *goldenHarness) *proxy.Service, next func(*proxy.Service) *proxy.Service) func(*proxy.Service, *goldenHarness) *proxy.Service {
	return func(svc *proxy.Service, h *goldenHarness) *proxy.Service { return next(base(svc, h)) }
}

func goldenToolResultScenarios() []goldenScenario {
	first := convUser(goldenConvOpening)
	call := convToolUse("toolu_1", "Bash", `{"command":"go test ./..."}`)
	result := convToolResult("toolu_1", "FAIL parser")
	sticky := convScenario("tool_result_turn_reuses_pin",
		convReq(first),
		convReq(first, call, result))
	sticky.decisions = []router.Decision{decision(providers.ProviderAnthropic, goldenAnthropicModel), decision(providers.ProviderAnthropic, goldenSiblingModel)}
	orphan := convScenario("orphaned_tool_call_sanitized",
		convReq(first, call, convUser("never mind, explain the failure instead")))
	duplicate := convScenario("duplicate_tool_result_coalesced",
		convReq(first, call, result, convToolResult("toolu_1", "FAIL parser")))
	return []goldenScenario{sticky, orphan, duplicate}
}

// goldenConversationStore records lifecycle store writes in trace order.
type goldenConversationStore struct {
	rec     *goldenRecorder
	enabled bool
}

func (s *goldenConversationStore) record(op string, detail any) {
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	s.rec.storeEvents = append(s.rec.storeEvents, goldenPinEvent{Op: op, Detail: goldenValue(detail)})
}

func (s *goldenConversationStore) InsertRouterFeedback(_ context.Context, p proxy.RouterFeedbackEvent) error {
	s.record("router_feedback.insert", p)
	return nil
}

func (s *goldenConversationStore) InsertLoopEscalationEvent(_ context.Context, p proxy.LoopEscalationEvent) error {
	s.record("loop_escalation.insert", p)
	return nil
}

func (s *goldenConversationStore) CountLoopEscalationEvents(context.Context, []byte, string) (int64, error) {
	return 0, nil
}

func (s *goldenConversationStore) InsertSpiralShadowEvent(_ context.Context, p proxy.SpiralShadowEvent) error {
	s.record("spiral_shadow.insert", p)
	return nil
}

func (s *goldenConversationStore) CountSpiralShadowEvents(context.Context, []byte, string, string) (int64, error) {
	return 0, nil
}

func (s *goldenConversationStore) InsertStruggleShadowEvent(_ context.Context, p proxy.StruggleShadowEvent) error {
	s.record("struggle_shadow.insert", p)
	return nil
}

func (s *goldenConversationStore) CountStruggleShadowEvents(context.Context, []byte, string, string) (int64, error) {
	return 0, nil
}

func (s *goldenConversationStore) Get(_ context.Context, installationID uuid.UUID, key [sessionstrategy.SessionKeyLen]byte) (sessionstrategy.Preference, bool, error) {
	if !s.enabled {
		return sessionstrategy.Preference{}, false, nil
	}
	return sessionstrategy.Preference{InstallationID: installationID, SessionKey: key, Strategy: router.StrategyHMMBeta}, true, nil
}

func (s *goldenConversationStore) Toggle(_ context.Context, p sessionstrategy.Preference) (bool, error) {
	s.record("session_strategy.toggle", p.Strategy)
	s.enabled = !s.enabled
	return s.enabled, nil
}

func (s *goldenConversationStore) Disable(_ context.Context, _ uuid.UUID, _ [sessionstrategy.SessionKeyLen]byte, strategy router.Strategy) (bool, error) {
	s.record("session_strategy.disable", strategy)
	was := s.enabled
	s.enabled = false
	return was, nil
}

// goldenSummarizer is a scripted compaction-handover summarizer.
type goldenSummarizer struct {
	rec     *goldenRecorder
	summary string
	err     error
}

func (g *goldenSummarizer) Provider() string { return providers.ProviderAnthropic }

func (g *goldenSummarizer) Summarize(_ context.Context, env *translate.RequestEnvelope, scope router.Request) (string, handover.Usage, error) {
	g.rec.mu.Lock()
	g.rec.storeEvents = append(g.rec.storeEvents, goldenPinEvent{Op: "handover.summarize", Detail: map[string]any{
		"requested_model": scope.RequestedModel, "estimated_tokens": env.FullTokenEstimate(),
	}})
	g.rec.mu.Unlock()
	return g.summary, handover.Usage{}, g.err
}
