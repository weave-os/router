package proxy_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/feedback"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
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
	return requestcontext.WithClientIdentity(ctx, requestcontext.ClientIdentity{ClientApp: requestcontext.ClientAppClaudeCode, SessionID: "golden-session"})
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

func withConvStores(sc goldenScenario) goldenScenario {
	sc.configure = func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
		return svc.WithRouterFeedbackStore(&goldenConversationStore{rec: h.rec})
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
		conversationCmd(first, reply, conversationUser("/router-feedback the parser plan was a great pick"))))
	feedbackEmpty := withConvStores(conversationScenario("router_feedback_command_without_text",
		conversationCmd(first, reply, conversationUser("/router-feedback"))))
	openAIFeedback := withConvStores(goldenScenario{
		name: ingressOpenAI + "_conversation_router_feedback_command", ingress: ingressOpenAI,
		decisions: []router.Decision{decision(providers.ProviderAnthropic, goldenAnthropicModel)},
		upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(false, "never")}},
		requests: []goldenRequest{{body: conversationOpenAIBody(conversationOpenAIUser(goldenConversationOpening),
			conversationOpenAIAssistant("plan"), conversationOpenAIUser("/router-feedback wrong model for this task")), noTelemetry: true}},
	})
	betaPrompt := conversationScenario("retired_beta_command_reaches_upstream", inSession(conversationReq(first, reply, conversationUser("/beta"))))
	legacyBeta := conversationScenario("legacy_beta_history_stripped_from_upstream",
		conversationReq(first, reply, conversationUser("/beta"), conversationAssistant("✦ **Weave Router** → Beta enabled. Type /beta again to turn it off.\n\n"), conversationUser("now add tests")))
	historical := conversationScenario("historical_command_stripped_from_upstream",
		conversationReq(first, reply, conversationUser("/router-session"), conversationAssistant("session details"), conversationUser("now add tests")))
	return []goldenScenario{forced, forcedStream, unforce, unknown, openAIForce, session, sessionWithID, models,
		feedback, feedbackEmpty, openAIFeedback, betaPrompt, legacyBeta, historical}
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

func clientTrimScenario(name string) goldenScenario {
	long := []string{conversationUser(goldenConversationOpening)}
	for i := range 6 {
		long = append(long, conversationAssistant(fmt.Sprintf("step %d done", i)), conversationUser(fmt.Sprintf("next step %d", i)))
	}
	short := []string{conversationUser(goldenConversationOpening), conversationAssistant("Summary: parser split into lexer and grammar."), conversationUser("keep going")}
	return goldenScenario{
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
			ctx = conversation.WithHandoffPreparation(ctx)
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
		clientTrimScenario("client_trim_keeps_compacted_history"),
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
	stuck := conversationRepeatedToolCalls(1, "Bash", `{"command":"go test ./..."}`)
	identicalReqs := make([]goldenRequest, 5)
	for i := range identicalReqs {
		identicalReqs[i] = conversationReq(stuck...)
	}
	identicalTurns := conversationScenario("identical_tool_turns_are_served", identicalReqs...)

	repeated := "I will now run the parser tests again to confirm the fix works as expected."
	repetition := []string{conversationUser(goldenConversationOpening)}
	for i := range 3 {
		id := fmt.Sprintf("toolu_r%d", i)
		repetition = append(repetition,
			fmt.Sprintf(`{"role":"assistant","content":[{"type":"text","text":%s},{"type":"tool_use","id":%q,"name":"Bash","input":{"command":"go test ./parser/%d"}}]}`, quoteJSON(repeated), id, i),
			conversationToolResult(id, fmt.Sprintf("ok %d", i)))
	}
	repeatedNarration := conversationScenario("repeated_narration_is_served", conversationReq(repetition...))
	openAIRep := []string{conversationOpenAIUser(goldenConversationOpening)}
	for i := range 3 {
		id := fmt.Sprintf("call_r%d", i)
		openAIRep = append(openAIRep,
			fmt.Sprintf(`{"role":"assistant","content":%s,"tool_calls":[{"id":%q,"type":"function","function":{"name":"Bash","arguments":"{\"command\":\"go test ./parser/%d\"}"}}]}`, quoteJSON(repeated), id, i),
			fmt.Sprintf(`{"role":"tool","tool_call_id":%q,"content":"ok %d"}`, id, i))
	}
	openAIRepeatedNarration := goldenScenario{
		name: ingressOpenAI + "_conversation_repeated_narration_is_served", ingress: ingressOpenAI,
		decisions: []router.Decision{decision(providers.ProviderAnthropic, goldenAnthropicModel)},
		upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(false, "ok")}},
		requests: []goldenRequest{{body: conversationOpenAIBody(openAIRep...), decorateContext: func(ctx context.Context) context.Context {
			return requestcontext.WithClientIdentity(ctx, requestcontext.ClientIdentity{ClientApp: requestcontext.ClientAppOpencode})
		}}},
	}

	return []goldenScenario{identicalTurns, repeatedNarration, openAIRepeatedNarration}
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
	rec *goldenRecorder
}

func (s *goldenConversationStore) record(op string, detail any) {
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	s.rec.storeEvents = append(s.rec.storeEvents, goldenPinEvent{Op: op, Detail: goldenValue(detail)})
}

func (s *goldenConversationStore) InsertRouterFeedback(_ context.Context, p feedback.RouterFeedbackEvent) error {
	s.record("router_feedback.insert", p)
	return nil
}
