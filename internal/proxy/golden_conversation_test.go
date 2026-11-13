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

const goldenConversationPrefix = ingressAnthropic + "_conversation_"

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

func conversationUser(text string) string {
	return fmt.Sprintf(`{"role":"user","content":%s}`, quoteJSON(text))
}

func conversationAssistant(text string) string {
	return fmt.Sprintf(`{"role":"assistant","content":%s}`, quoteJSON(text))
}

func conversationToolUse(id, name, input string) string {
	return fmt.Sprintf(`{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":%q,"input":%s}]}`, id, name, input)
}

func conversationToolResult(id, content string) string {
	return fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":%s}]}`, id, quoteJSON(content))
}

const goldenConversationTools = `[{"name":"Bash","description":"run","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}},` +
	`{"name":"Read","description":"read","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}},` +
	`{"name":"Edit","description":"edit","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]`

func conversationBody(stream bool, messages ...string) string {
	return fmt.Sprintf(`{"model":%q,"stream":%t,"max_tokens":1024,"tools":%s,"messages":[%s]}`,
		goldenAnthropicModel, stream, goldenConversationTools, strings.Join(messages, ","))
}

func conversationOpenAIBody(messages ...string) string {
	return fmt.Sprintf(`{"model":%q,"stream":false,"messages":[%s]}`, goldenAnthropicModel, strings.Join(messages, ","))
}

func conversationOpenAIUser(text string) string {
	return fmt.Sprintf(`{"role":"user","content":%s}`, quoteJSON(text))
}

func conversationOpenAIAssistant(text string) string {
	return fmt.Sprintf(`{"role":"assistant","content":%s}`, quoteJSON(text))
}

const goldenConversationOpening = "refactor the parser package"

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

func conversationScenario(name string, requests ...goldenRequest) goldenScenario {
	return goldenScenario{
		name: goldenConversationPrefix + name, ingress: ingressAnthropic,
		decisions: []router.Decision{decision(providers.ProviderAnthropic, goldenAnthropicModel)},
		upstreams: map[string][]goldenStep{providers.ProviderAnthropic: anthropicSteps(len(requests))},
		requests:  requests,
	}
}

func conversationReq(messages ...string) goldenRequest {
	return goldenRequest{body: conversationBody(false, messages...)}
}

// conversationCmd is a turn answered by the router itself, which emits no request row.
func conversationCmd(messages ...string) goldenRequest {
	return goldenRequest{body: conversationBody(false, messages...), noTelemetry: true}
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
	first := conversationUser(goldenConversationOpening)
	reply := conversationAssistant("Here is the plan for the parser package refactor.")
	forced := conversationScenario("force_model_command_then_forced_turn",
		conversationCmd(first, reply, conversationUser("/force-model "+goldenSiblingModel)),
		conversationReq(first, reply, conversationUser("/force-model "+goldenSiblingModel), conversationAssistant("forced"), conversationUser("continue")))
	forced.upstreams[providers.ProviderAnthropic] = []goldenStep{anthropicOK(false, "forced reply")}
	forcedStream := conversationScenario("force_model_command_stream_ack",
		goldenRequest{body: conversationBody(true, first, reply, conversationUser("/force-model "+goldenSiblingModel)), noTelemetry: true})
	unforce := conversationScenario("unforce_model_command_returns_to_routing",
		conversationCmd(first, reply, conversationUser("/force-model "+goldenSiblingModel)),
		conversationCmd(first, reply, conversationUser("/unforce-model")),
		conversationReq(first, reply, conversationUser("/unforce-model"), conversationAssistant("unforced"), conversationUser("continue")))
	unknown := conversationScenario("force_model_command_unknown_model", conversationCmd(first, reply, conversationUser("/force-model not-a-real-model")))
	openAIForce := goldenScenario{
		name: ingressOpenAI + "_conversation_force_model_command_ack", ingress: ingressOpenAI,
		decisions: []router.Decision{decision(providers.ProviderAnthropic, goldenAnthropicModel)},
		upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(false, "never")}},
		requests: []goldenRequest{{body: conversationOpenAIBody(conversationOpenAIUser(goldenConversationOpening),
			conversationOpenAIAssistant("plan"), conversationOpenAIUser("/force-model "+goldenSiblingModel)), noTelemetry: true}},
	}
	session := conversationScenario("router_session_command", conversationCmd(first, reply, conversationUser("/router-session")))
	sessionWithID := conversationScenario("router_session_command_with_client_session", inSession(conversationCmd(first, reply, conversationUser("/router-session"))))
	models := conversationScenario("router_models_command", conversationCmd(first, reply, conversationUser("/router-models")))
	feedback := withConvStores(conversationScenario("router_feedback_command_records_event",
		conversationCmd(first, reply, conversationUser("/router-feedback the parser plan was a great pick"))), false)
	feedbackEmpty := withConvStores(conversationScenario("router_feedback_command_without_text",
		conversationCmd(first, reply, conversationUser("/router-feedback"))), false)
	openAIFeedback := withConvStores(goldenScenario{
		name: ingressOpenAI + "_conversation_router_feedback_command", ingress: ingressOpenAI,
		decisions: []router.Decision{decision(providers.ProviderAnthropic, goldenAnthropicModel)},
		upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(false, "never")}},
		requests: []goldenRequest{{body: conversationOpenAIBody(conversationOpenAIUser(goldenConversationOpening),
			conversationOpenAIAssistant("plan"), conversationOpenAIUser("/router-feedback wrong model for this task")), noTelemetry: true}},
	}, false)
	betaNoStore := conversationScenario("beta_command_without_store", conversationCmd(first, reply, conversationUser("/beta")))
	betaDisable := withConvStores(conversationScenario("beta_command_disables_unavailable_strategy",
		inSession(conversationCmd(first, reply, conversationUser("/beta")))), true)
	historical := conversationScenario("historical_command_stripped_from_upstream",
		conversationReq(first, reply, conversationUser("/router-session"), conversationAssistant("session details"), conversationUser("now add tests")))
	return []goldenScenario{forced, forcedStream, unforce, unknown, openAIForce, session, sessionWithID, models,
		feedback, feedbackEmpty, openAIFeedback, betaNoStore, betaDisable, historical}
}

func goldenContinuationScenarios() []goldenScenario {
	first := conversationUser(goldenConversationOpening)
	reply := conversationAssistant("Here is the plan for the parser package refactor.")
	grant := conversationScenario("post_command_continuation_granted_and_consumed",
		conversationReq(first),
		conversationCmd(first, reply, conversationUser("/router-session")),
		conversationReq(first, reply, conversationUser("/router-session"), conversationAssistant("session details"), conversationUser("continue the refactor")))
	noPin := conversationScenario("post_command_continuation_skipped_without_pin",
		conversationCmd(first, reply, conversationUser("/router-session")),
		conversationReq(first, reply, conversationUser("/router-session"), conversationAssistant("session details"), conversationUser("continue the refactor")))
	forced := conversationScenario("post_command_continuation_skipped_when_forced",
		conversationCmd(first, reply, conversationUser("/force-model "+goldenSiblingModel)),
		conversationCmd(first, reply, conversationUser("/router-models")),
		conversationReq(first, reply, conversationUser("/router-models"), conversationAssistant("models"), conversationUser("continue the refactor")))
	expired := conversationScenario("post_command_continuation_expires",
		conversationReq(first),
		conversationCmd(first, reply, conversationUser("/router-session")),
		goldenRequest{advance: 2 * time.Hour, body: conversationBody(false, first, reply, conversationUser("/router-session"),
			conversationAssistant("session details"), conversationUser("continue the refactor"))})
	return []goldenScenario{grant, noPin, forced, expired}
}

func conversationLongHistory(turns int) []string {
	msgs := []string{conversationUser(goldenConversationOpening)}
	for i := range turns {
		msgs = append(msgs, conversationAssistant(fmt.Sprintf("step %d done", i)), conversationUser(fmt.Sprintf("next step %d", i)))
	}
	return msgs
}

func handoverScenario(name string, summarizer handover.Summarizer) goldenScenario {
	long := conversationLongHistory(6)
	short := []string{conversationUser(goldenConversationOpening), conversationAssistant("Summary: parser split into lexer and grammar."), conversationUser("keep going")}
	sc := goldenScenario{
		name: goldenConversationPrefix + name, ingress: ingressAnthropic,
		decisions: []router.Decision{decision(providers.ProviderFireworks, goldenCompatModel)},
		upstreams: map[string][]goldenStep{
			providers.ProviderFireworks: {compatOK(false, "first"), compatOK(false, "second")},
			providers.ProviderAnthropic: {anthropicOK(false, "never")},
		},
		requests: []goldenRequest{
			{body: conversationBody(false, long...), decorateContext: goldenSessionCtx},
			{body: conversationBody(false, short...), decorateContext: goldenSessionCtx},
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
	sc := conversationScenario(name, requests...)
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
		goldenAnthropicModel, conversationUser(goldenConversationOpening))
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
			goldenRequest{body: strings.Replace(body, conversationUser(goldenConversationOpening), strings.Join([]string{conversationUser(goldenConversationOpening), conversationAssistant("plan"), conversationUser("/force-model " + goldenSiblingModel)}, ","), 1),
				decorateContext: handoffCtx(true), noTelemetry: true}),
	}
}

func conversationRepeatedToolCalls(n int, name, input string) []string {
	msgs := []string{conversationUser(goldenConversationOpening)}
	for i := range n {
		id := fmt.Sprintf("toolu_%02d", i)
		msgs = append(msgs, conversationToolUse(id, name, input), conversationToolResult(id, "same output"))
	}
	return msgs
}

func goldenDetectionScenarios() []goldenScenario {
	loop := withConvStores(conversationScenario("cyclic_tool_loop_escalates",
		conversationReq(conversationRepeatedToolCalls(24, "Read", `{"path":"parser.go"}`)...)), false)
	loopDisabled := withConvStores(conversationScenario("cyclic_tool_loop_disabled_records_event",
		conversationReq(conversationRepeatedToolCalls(24, "Read", `{"path":"parser.go"}`)...)), false)
	base := loopDisabled.configure
	loopDisabled.configure = func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
		return base(svc, h).WithLoopEscalationConfig(false, 0)
	}
	withEdit := conversationRepeatedToolCalls(23, "Read", `{"path":"parser.go"}`)
	withEdit = append(withEdit, conversationToolUse("toolu_edit", "Edit", `{"path":"parser.go"}`), conversationToolResult("toolu_edit", "ok"))
	editProgress := withConvStores(conversationScenario("cyclic_tool_loop_with_edit_is_progress", conversationReq(withEdit...)), false)

	stuck := conversationRepeatedToolCalls(1, "Bash", `{"command":"go test ./..."}`)
	noProgressReqs := make([]goldenRequest, 5)
	for i := range noProgressReqs {
		noProgressReqs[i] = conversationReq(stuck...)
	}
	noProgressReqs[4].noTelemetry = true
	noProgress := conversationScenario("no_progress_break_after_identical_tool_turns", noProgressReqs...)

	repeated := "I will now run the parser tests again to confirm the fix works as expected."
	repetition := []string{conversationUser(goldenConversationOpening)}
	for i := range 3 {
		id := fmt.Sprintf("toolu_r%d", i)
		repetition = append(repetition,
			fmt.Sprintf(`{"role":"assistant","content":[{"type":"text","text":%s},{"type":"tool_use","id":%q,"name":"Bash","input":{"command":"go test ./parser/%d"}}]}`, quoteJSON(repeated), id, i),
			conversationToolResult(id, fmt.Sprintf("ok %d", i)))
	}
	repBreak := conversationReq(repetition...)
	repBreak.noTelemetry = true
	textRep := conversationScenario("text_repetition_break", repBreak)
	textRepOff := conversationScenario("text_repetition_break_disabled", conversationReq(repetition...))
	textRepOff.configure = func(svc *proxy.Service, _ *goldenHarness) *proxy.Service { return svc.WithTextRepetitionBreak(false) }
	openAIRep := []string{conversationOpenAIUser(goldenConversationOpening)}
	for i := range 3 {
		id := fmt.Sprintf("call_r%d", i)
		openAIRep = append(openAIRep,
			fmt.Sprintf(`{"role":"assistant","content":%s,"tool_calls":[{"id":%q,"type":"function","function":{"name":"Bash","arguments":"{\"command\":\"go test ./parser/%d\"}"}}]}`, quoteJSON(repeated), id, i),
			fmt.Sprintf(`{"role":"tool","tool_call_id":%q,"content":"ok %d"}`, id, i))
	}
	openAITextRep := goldenScenario{
		name: ingressOpenAI + "_conversation_text_repetition_break", ingress: ingressOpenAI,
		decisions: []router.Decision{decision(providers.ProviderAnthropic, goldenAnthropicModel)},
		upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(false, "never")}},
		requests: []goldenRequest{{body: conversationOpenAIBody(openAIRep...), noTelemetry: true, decorateContext: func(ctx context.Context) context.Context {
			return requestcontext.WithClientIdentity(ctx, proxy.ClientIdentity{ClientApp: proxy.ClientAppOpencode})
		}}},
	}

	erroredTurns := []string{conversationUser(goldenConversationOpening)}
	for i := range 12 {
		id := fmt.Sprintf("toolu_e%02d", i)
		erroredTurns = append(erroredTurns, conversationToolUse(id, "Bash", fmt.Sprintf(`{"command":"go build ./pkg%d"}`, i)),
			fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"is_error":true,"content":"build failed"}]}`, id))
	}
	consented := goldenRequest{body: conversationBody(false, erroredTurns...), decorateContext: func(ctx context.Context) context.Context {
		return context.WithValue(ctx, proxy.PolicyTrainingAllowedContextKey{}, true)
	}}
	spiral := withConvStores(conversationScenario("spiral_shadow_records_error_streak", consented), false)
	spiral.configure = chainConfigure(spiral.configure, func(svc *proxy.Service) *proxy.Service {
		return svc.WithSpiralShadowConfig(true).WithContentCapture(proxy.CaptureHashed, 0, nil)
	})
	spiralNoConsent := withConvStores(conversationScenario("spiral_shadow_skipped_without_training_consent", conversationReq(erroredTurns...)), false)

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
	struggle := withConvStores(conversationScenario("struggle_shadow_records_long_session",
		conversationReq(conversationLongHistory(1)...),
		goldenRequest{body: conversationBody(false, conversationLongHistory(2)...), decorateContext: agedPin}), false)
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
	first := conversationUser(goldenConversationOpening)
	call := conversationToolUse("toolu_1", "Bash", `{"command":"go test ./..."}`)
	result := conversationToolResult("toolu_1", "FAIL parser")
	sticky := conversationScenario("tool_result_turn_reuses_pin",
		conversationReq(first),
		conversationReq(first, call, result))
	sticky.decisions = []router.Decision{decision(providers.ProviderAnthropic, goldenAnthropicModel), decision(providers.ProviderAnthropic, goldenSiblingModel)}
	orphan := conversationScenario("orphaned_tool_call_sanitized",
		conversationReq(first, call, conversationUser("never mind, explain the failure instead")))
	duplicate := conversationScenario("duplicate_tool_result_coalesced",
		conversationReq(first, call, result, conversationToolResult("toolu_1", "FAIL parser")))
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

// goldenSummarizer returns a fixed summary or error and records each call as a
// handover.summarize store event.
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
