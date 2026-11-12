package proxy_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/breaker"
)

const (
	goldenAnthropicModel = "claude-haiku-4-5"
	goldenSiblingModel   = "claude-opus-4-7"
	goldenCompatModel    = "deepseek/deepseek-v4-pro"
	goldenCompatWire     = "accounts/fireworks/models/deepseek-v4-pro"
	goldenSubscription   = "sk-ant-oat01-golden-subscription"
)

func goldenAnthropicBody(stream bool) string {
	return fmt.Sprintf(`{"model":"claude-sonnet-4-5","max_tokens":4096,"stream":%t,"messages":[{"role":"user","content":"Say hi"}]}`, stream)
}

func goldenOpenAIBody(stream bool) string {
	return fmt.Sprintf(`{"model":"gpt-5","stream":%t,"messages":[{"role":"user","content":"Say hi"}]}`, stream)
}

func goldenSSE(event, data string) string {
	if event == "" {
		return "data: " + data + "\n\n"
	}
	return "event: " + event + "\ndata: " + data + "\n\n"
}

// goldenAnthropicStream is a native Messages stream.
func goldenAnthropicStream(model, text string) []string {
	return []string{
		goldenSSE("message_start", `{"type":"message_start","message":{"id":"msg_up","type":"message","role":"assistant","model":"`+model+`","content":[],"stop_reason":null,"usage":{"input_tokens":12,"output_tokens":1}}}`),
		goldenSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		goldenSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+text+`"}}`),
		goldenSSE("content_block_stop", `{"type":"content_block_stop","index":0}`),
		goldenSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`),
		goldenSSE("message_stop", `{"type":"message_stop"}`),
	}
}

func goldenAnthropicJSON(model, text string) []string {
	return []string{`{"id":"msg_up","type":"message","role":"assistant","model":"` + model + `","content":[{"type":"text","text":"` + text + `"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":7}}`}
}

func goldenOK(contentType string, frames []string) goldenStep {
	return goldenStep{status: http.StatusOK, headers: map[string]string{"Content-Type": contentType}, frames: frames}
}

func anthropicOK(stream bool, text string) goldenStep {
	if stream {
		return goldenOK("text/event-stream", goldenAnthropicStream(goldenAnthropicModel, text))
	}
	return goldenOK("application/json", goldenAnthropicJSON(goldenAnthropicModel, text))
}

func decision(provider, model string) router.Decision {
	return router.Decision{Provider: provider, Model: model, Reason: "golden_route"}
}

func streamName(stream bool) string {
	if stream {
		return "stream"
	}
	return "nonstream"
}

func ingressBody(ingress string, stream bool) string {
	if ingress == ingressOpenAI {
		return goldenOpenAIBody(stream)
	}
	return goldenAnthropicBody(stream)
}

// goldenCompatStream is an OpenAI-compatible chat-completions stream.
func goldenCompatStream(model, text string) []string {
	chunk := func(delta, finish string) string {
		return goldenSSE("", `{"id":"chatcmpl-up","object":"chat.completion.chunk","model":"`+model+`","choices":[{"index":0,"delta":`+delta+`,"finish_reason":`+finish+`}]}`)
	}
	return []string{
		chunk(`{"role":"assistant","content":""}`, "null"),
		chunk(`{"content":"`+text+`"}`, "null"),
		chunk(`{}`, `"stop"`),
		goldenSSE("", `{"id":"chatcmpl-up","object":"chat.completion.chunk","model":"`+model+`","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":7,"total_tokens":19}}`),
		goldenSSE("", "[DONE]"),
	}
}

func goldenCompatJSON(model, text string) []string {
	return []string{`{"id":"chatcmpl-up","object":"chat.completion","model":"` + model + `","choices":[{"index":0,"message":{"role":"assistant","content":"` + text + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":7,"total_tokens":19}}`}
}

func compatOK(stream bool, text string) goldenStep {
	if stream {
		return goldenOK("text/event-stream", goldenCompatStream(goldenCompatWire, text))
	}
	return goldenOK("application/json", goldenCompatJSON(goldenCompatWire, text))
}

// withCandidates adds same-cluster candidates the proxy may rescue onto.
func withCandidates(d router.Decision, candidates map[string]string, order ...string) router.Decision {
	d.Metadata = &router.RoutingMetadata{PolicyGroup: "golden-group", CandidateModels: order, CandidateProviders: candidates}
	return d
}

var (
	goldenCompatDecision    = decision(providers.ProviderFireworks, goldenCompatModel)
	goldenAnthropicDecision = decision(providers.ProviderAnthropic, goldenAnthropicModel)
	goldenRescueDecision    = withCandidates(decision(providers.ProviderAnthropic, goldenSiblingModel),
		map[string]string{goldenSiblingModel: providers.ProviderAnthropic, goldenCompatModel: providers.ProviderFireworks},
		goldenSiblingModel, goldenCompatModel)
	goldenOverflowBody = `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 250000 tokens > 200000 maximum"}}`
	goldenOverloadBody = `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
)

// goldenMatrixScenarios run on both ingresses, streamed and not.
func goldenMatrixScenarios(ingress string, stream bool) []goldenScenario {
	prefix := ingress + "_" + streamName(stream) + "_"
	singleRequest := []goldenRequest{{body: ingressBody(ingress, stream)}}
	sc := func(name string, d router.Decision, upstreams map[string][]goldenStep) goldenScenario {
		return goldenScenario{name: prefix + name, ingress: ingress, decisions: []router.Decision{d}, upstreams: upstreams, requests: singleRequest}
	}
	return []goldenScenario{
		sc("anthropic_success", goldenAnthropicDecision, map[string][]goldenStep{
			providers.ProviderAnthropic: {anthropicOK(stream, "hi")}}),
		sc("compat_success", goldenCompatDecision, map[string][]goldenStep{
			providers.ProviderFireworks: {compatOK(stream, "hi")}}),
		sc("rate_limited_binding_failover", goldenCompatDecision, map[string][]goldenStep{
			providers.ProviderFireworks:  {goldenUpstreamError(http.StatusTooManyRequests, `{"error":{"message":"rate limited"}}`)},
			providers.ProviderOpenRouter: {compatOK(stream, "rescued")}}),
		sc("server_error_binding_failover", goldenCompatDecision, map[string][]goldenStep{
			providers.ProviderFireworks:  {goldenUpstreamError(http.StatusServiceUnavailable, `{"error":{"message":"unavailable"}}`)},
			providers.ProviderOpenRouter: {compatOK(stream, "rescued")}}),
		sc("timeout_binding_failover", goldenCompatDecision, map[string][]goldenStep{
			providers.ProviderFireworks:  {{err: &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}}},
			providers.ProviderOpenRouter: {compatOK(stream, "rescued")}}),
		sc("bindings_exhausted", goldenCompatDecision, map[string][]goldenStep{
			providers.ProviderFireworks:  {goldenUpstreamError(http.StatusBadGateway, `{"error":{"message":"bad gateway"}}`)},
			providers.ProviderOpenRouter: {goldenUpstreamError(http.StatusServiceUnavailable, `{"error":{"message":"unavailable"}}`)}}),
		sc("overloaded_sibling_rescue", goldenRescueDecision, map[string][]goldenStep{
			providers.ProviderAnthropic: {goldenUpstreamError(529, goldenOverloadBody)},
			providers.ProviderFireworks: {compatOK(stream, "rescued")}}),
		sc("upstream_context_overflow", goldenRescueDecision, map[string][]goldenStep{
			providers.ProviderAnthropic: {goldenUpstreamError(http.StatusBadRequest, goldenOverflowBody)},
			providers.ProviderFireworks: {compatOK(stream, "rescued")}}),
		sc("unbound_provider_decision", decision(providers.ProviderFireworks, goldenAnthropicModel), map[string][]goldenStep{
			providers.ProviderFireworks: {compatOK(stream, "served")},
			providers.ProviderAnthropic: {anthropicOK(stream, "served")}}),
		{name: prefix + "prompt_past_primary_window", ingress: ingress,
			decisions: []router.Decision{withCandidates(goldenAnthropicDecision,
				map[string]string{goldenAnthropicModel: providers.ProviderAnthropic, goldenCompatModel: providers.ProviderFireworks},
				goldenAnthropicModel, goldenCompatModel), goldenCompatDecision},
			upstreams: map[string][]goldenStep{
				providers.ProviderAnthropic: {anthropicOK(stream, "never")},
				providers.ProviderFireworks: {compatOK(stream, "wide")}},
			configure: func(svc *proxy.Service, _ *goldenHarness) *proxy.Service {
				return svc.WithAvailableModels(map[string]struct{}{goldenAnthropicModel: {}, goldenCompatModel: {}})
			},
			requests: []goldenRequest{{body: strings.Replace(ingressBody(ingress, stream), "Say hi", strings.Repeat("overflow ", 120_000), 1)}}},
	}
}

// goldenStreamScenarios exercise the stream-commit boundary.
func goldenStreamScenarios(ingress string) []goldenScenario {
	prefix := ingress + "_stream_"
	singleRequest := []goldenRequest{{body: ingressBody(ingress, true)}}
	compat := goldenCompatStream(goldenCompatWire, "partial")
	native := goldenAnthropicStream(goldenAnthropicModel, "partial")
	eventStreamHeaders := map[string]string{"Content-Type": "text/event-stream"}
	return []goldenScenario{
		{name: prefix + "cut_after_role_chunk", ingress: ingress, requests: singleRequest,
			decisions: []router.Decision{goldenCompatDecision},
			upstreams: map[string][]goldenStep{
				providers.ProviderFireworks:  {{status: http.StatusOK, headers: eventStreamHeaders, frames: compat[:1], err: io.ErrUnexpectedEOF}},
				providers.ProviderOpenRouter: {compatOK(true, "rescued")}}},
		{name: prefix + "cut_after_content_chunk", ingress: ingress, requests: singleRequest,
			decisions: []router.Decision{goldenCompatDecision},
			upstreams: map[string][]goldenStep{
				providers.ProviderFireworks:  {{status: http.StatusOK, headers: eventStreamHeaders, frames: compat[:2], err: io.ErrUnexpectedEOF}},
				providers.ProviderOpenRouter: {compatOK(true, "never")}}},
		{name: prefix + "native_cut_after_content", ingress: ingress, requests: singleRequest,
			decisions: []router.Decision{goldenAnthropicDecision},
			upstreams: map[string][]goldenStep{
				providers.ProviderAnthropic: {{status: http.StatusOK, headers: eventStreamHeaders, frames: native[:3], err: io.ErrUnexpectedEOF}}}},
		{name: prefix + "client_cancel_mid_stream", ingress: ingress, requests: singleRequest,
			decisions: []router.Decision{goldenAnthropicDecision},
			upstreams: map[string][]goldenStep{
				providers.ProviderAnthropic: {{status: http.StatusOK, headers: eventStreamHeaders, frames: native[:3], cancel: true}}}},
	}
}

func goldenSubscriptionCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, proxy.AnthropicSubscriptionContextKey{}, goldenSubscription)
}

// exhaustSubscription records a spent weekly window for the golden token. The
// observer reads the wall clock because Service compares quota windows against
// its own unexported clock.
func exhaustSubscription(svc *proxy.Service, _ *goldenHarness) *proxy.Service {
	obs := usage.NewObserver([]byte("golden-salt"), 10*time.Minute, time.Now)
	obs.Record(obs.Key([]byte(goldenSubscription)), usage.Snapshot{Secondary: usage.Window{UsedPercent: 1.0, WindowMinutes: 10080}})
	return svc.WithUsageObserver(obs)
}

const goldenClassifierBody = `{"model":"claude-haiku-4-5","max_tokens":64,"messages":[{"role":"user","content":"Rate this"}]}`

const goldenToolResultBody = `{"model":"claude-opus-4-7","stream":true,"max_tokens":4096,
"tools":[{"name":"Bash","description":"run","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],
"messages":[
{"role":"user","content":"fix the three failing tests"},
{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./..."}}]},
{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"FAIL a\nFAIL b\nFAIL c"}]}]}`

func goldenClaudeCodeCtx(ctx context.Context) context.Context {
	return requestcontext.WithClientIdentity(ctx, proxy.ClientIdentity{ClientApp: proxy.ClientAppClaudeCode})
}

// goldenAnthropicOnlyScenarios cover behavior that only the Messages ingress
// or cross-request state exercises.
func goldenAnthropicOnlyScenarios() []goldenScenario {
	eventStreamHeaders := map[string]string{"Content-Type": "text/event-stream"}
	stopped := goldenAnthropicStream(goldenAnthropicModel, "Fixed test a. Next I will fix b and c.")
	toolCall := []string{
		goldenSSE("message_start", `{"type":"message_start","message":{"id":"msg_up","type":"message","role":"assistant","model":"`+goldenSiblingModel+`","content":[],"stop_reason":null,"usage":{"input_tokens":12,"output_tokens":1}}}`),
		goldenSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_2","name":"Bash","input":{}}}`),
		goldenSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}`),
		goldenSSE("content_block_stop", `{"type":"content_block_stop","index":0}`),
		goldenSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`),
		goldenSSE("message_stop", `{"type":"message_stop"}`),
	}
	earlyStop := withCandidates(goldenAnthropicDecision,
		map[string]string{goldenAnthropicModel: providers.ProviderAnthropic, goldenSiblingModel: providers.ProviderAnthropic},
		goldenAnthropicModel, goldenSiblingModel)
	breakerCfg := breaker.Config{Strikes: 2, Window: time.Minute, OrgConfirmations: 1, RecoveryBase: time.Minute, RecoveryCap: time.Minute}
	body := goldenAnthropicBody(true)
	return []goldenScenario{
		{name: "anthropic_messages_stream_early_stop_redo", ingress: ingressAnthropic,
			decisions: []router.Decision{earlyStop},
			upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {
				{status: http.StatusOK, headers: eventStreamHeaders, frames: stopped},
				{status: http.StatusOK, headers: eventStreamHeaders, frames: toolCall}}},
			requests: []goldenRequest{{body: goldenToolResultBody, decorateContext: goldenClaudeCodeCtx}},
			configure: func(svc *proxy.Service, _ *goldenHarness) *proxy.Service {
				return svc.WithEarlyStopRedo(flags.EarlyStopRedoOn)
			}},
		{name: "anthropic_messages_nonstream_classifier_subscription_exhausted_paid_fallback", ingress: ingressAnthropic,
			decisions: []router.Decision{goldenRescueDecision},
			upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(false, "0")}},
			requests:  []goldenRequest{{body: goldenClassifierBody, decorateContext: goldenSubscriptionCtx}},
			configure: exhaustSubscription},
		{name: "anthropic_messages_stream_overload_breaker_cooldown", ingress: ingressAnthropic,
			decisions: []router.Decision{goldenRescueDecision, goldenCompatDecision},
			upstreams: map[string][]goldenStep{
				providers.ProviderAnthropic: {goldenUpstreamError(529, goldenOverloadBody)},
				providers.ProviderFireworks: {compatOK(true, "rescued")}},
			requests: []goldenRequest{{body: body}, {body: body}, {body: body}, {body: body, advance: 2 * time.Minute}},
			configure: func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
				return svc.WithProviderOverloaded(true, breaker.New(breakerCfg, h.clock.Now))
			}},
		{name: "anthropic_messages_nonstream_classifier_hard_pin", ingress: ingressAnthropic,
			decisions: []router.Decision{goldenRescueDecision},
			upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(false, "0")}},
			requests:  []goldenRequest{{body: goldenClassifierBody}}},
	}
}

func goldenInferenceScenarios() []goldenScenario {
	var out []goldenScenario
	for _, ingress := range []string{ingressAnthropic, ingressOpenAI} {
		for _, stream := range []bool{true, false} {
			out = append(out, goldenMatrixScenarios(ingress, stream)...)
		}
		out = append(out, goldenStreamScenarios(ingress)...)
		out = append(out, goldenScenario{
			name: ingress + "_nonstream_route_error", ingress: ingress, routeErr: errors.New("no eligible model"),
			upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(false, "never")}},
			requests:  []goldenRequest{{body: ingressBody(ingress, false), noTelemetry: true}},
		})
	}
	return append(out, goldenAnthropicOnlyScenarios()...)
}
