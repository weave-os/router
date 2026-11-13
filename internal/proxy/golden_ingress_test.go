package proxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	geminiapi "weave-os/router/internal/api/gemini"
	openaiapi "weave-os/router/internal/api/openai"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

// Handler-driven ingresses run the real gin handler, so the client bytes
// include the error envelope each surface renders from a returned error.
const (
	ingressResponses = "openai_responses"
	ingressGemini    = "gemini_generate_content"

	goldenGeminiModel = "gemini-2.5-flash"
	goldenOpenAIModel = "gpt-5.5"
	goldenCodexUA     = "codex_cli_rs/0.50.0"
)

type goldenIngressSurface struct {
	path  string
	serve func(svc *proxy.Service, ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) error
}

var goldenIngressSurfaces = map[string]goldenIngressSurface{
	ingressAnthropic: {path: "/v1/messages", serve: (*proxy.Service).ProxyMessages},
	ingressOpenAI:    {path: "/v1/chat/completions", serve: (*proxy.Service).ProxyOpenAIChatCompletion},
	ingressResponses: {path: "/v1/responses", serve: goldenHandler("/v1/responses", openaiapi.ResponsesHandler)},
	ingressGemini:    {path: goldenGeminiPath(false), serve: goldenHandler("/v1beta/models/:modelAction", geminiapi.GenerateContentHandler)},
}

func init() { gin.SetMode(gin.TestMode) }

// goldenHandler serves the request through the production handler with no
// auth service, which the handlers treat as an unauthenticated installation.
func goldenHandler(route string, handler func(*proxy.Service, *auth.Service) gin.HandlerFunc) func(*proxy.Service, context.Context, []byte, http.ResponseWriter, *http.Request) error {
	return func(svc *proxy.Service, _ context.Context, _ []byte, w http.ResponseWriter, r *http.Request) error {
		engine := gin.New()
		engine.POST(route, handler(svc, nil))
		engine.ServeHTTP(w, r)
		return nil
	}
}

func goldenGeminiPath(stream bool) string {
	if stream {
		return "/v1beta/models/" + goldenGeminiModel + ":streamGenerateContent"
	}
	return "/v1beta/models/" + goldenGeminiModel + ":generateContent"
}

func goldenResponsesBody(stream bool) string {
	return `{"model":"gpt-5","stream":` + boolJSON(stream) + `,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Say hi"}]}]}`
}

func goldenGeminiBody() string {
	return `{"contents":[{"role":"user","parts":[{"text":"Say hi"}]}]}`
}

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func goldenGeminiCandidate(text, finish string) string {
	finishField := ""
	if finish != "" {
		finishField = `,"finishReason":"` + finish + `"`
	}
	return `{"candidates":[{"content":{"role":"model","parts":[{"text":"` + text + `"}]},"index":0` + finishField + `}],"modelVersion":"` + goldenGeminiModel + `"`
}

const goldenGeminiUsage = `"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":7,"totalTokenCount":19}`

func goldenGeminiStream(text string) []string {
	return []string{
		goldenSSE("", goldenGeminiCandidate(text, "")+"}"),
		goldenSSE("", goldenGeminiCandidate("", "STOP")+","+goldenGeminiUsage+"}"),
	}
}

func geminiOK(stream bool, text string) goldenStep {
	if stream {
		return goldenOK("text/event-stream", goldenGeminiStream(text))
	}
	return goldenOK("application/json", []string{goldenGeminiCandidate(text, "STOP") + "," + goldenGeminiUsage + "}"})
}

func goldenResponsesStream(model, text string) []string {
	item := `{"type":"message","id":"msg_up","status":"completed","role":"assistant","content":[{"type":"output_text","text":"` + text + `","annotations":[]}]}`
	return []string{
		goldenSSE("response.created", `{"type":"response.created","response":{"id":"resp_up","object":"response","status":"in_progress","model":"`+model+`","output":[]}}`),
		goldenSSE("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_up","status":"in_progress","role":"assistant","content":[]}}`),
		goldenSSE("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_up","output_index":0,"content_index":0,"delta":"`+text+`"}`),
		goldenSSE("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":`+item+`}`),
		goldenSSE("response.completed", `{"type":"response.completed","response":{"id":"resp_up","object":"response","status":"completed","model":"`+model+`","output":[`+item+`],"usage":{"input_tokens":12,"output_tokens":7,"total_tokens":19}}}`),
	}
}

func goldenResponsesJSON(model, text string) []string {
	return []string{`{"id":"resp_up","object":"response","status":"completed","model":"` + model + `","output":[{"type":"message","id":"msg_up","status":"completed","role":"assistant","content":[{"type":"output_text","text":"` + text + `","annotations":[]}]}],"usage":{"input_tokens":12,"output_tokens":7,"total_tokens":19}}`}
}

func responsesOK(stream bool, text string) goldenStep {
	if stream {
		return goldenOK("text/event-stream", goldenResponsesStream(goldenOpenAIModel, text))
	}
	return goldenOK("application/json", goldenResponsesJSON(goldenOpenAIModel, text))
}

var (
	goldenGeminiDecision = decision(providers.ProviderGoogle, goldenGeminiModel)
	goldenOpenAIDecision = decision(providers.ProviderOpenAI, goldenOpenAIModel)
	goldenEventStream    = map[string]string{"Content-Type": "text/event-stream"}
	goldenCodexHeaders   = map[string]string{"User-Agent": goldenCodexUA}
)

// goldenChatToResponses rewrites an OpenAI Chat request into the equivalent
// Responses request so the Chat matrix can be replayed on /v1/responses.
func goldenChatToResponses(body string) string {
	var chat struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &chat); err != nil {
		panic(err)
	}
	input := make([]map[string]any, 0, len(chat.Messages))
	for _, m := range chat.Messages {
		input = append(input, map[string]any{"type": "message", "role": m.Role,
			"content": []map[string]string{{"type": "input_text", "text": m.Content}}})
	}
	out, _ := json.Marshal(map[string]any{"model": chat.Model, "stream": chat.Stream, "input": input})
	return string(out)
}

// goldenAsResponses replays Chat-ingress scenarios through the Responses
// handler, keeping their decisions and upstream scripts.
func goldenAsResponses(scenarios []goldenScenario) []goldenScenario {
	out := make([]goldenScenario, 0, len(scenarios))
	for _, sc := range scenarios {
		sc.name = ingressResponses + strings.TrimPrefix(sc.name, ingressOpenAI)
		sc.ingress = ingressResponses
		reqs := make([]goldenRequest, len(sc.requests))
		for i, req := range sc.requests {
			req.body = goldenChatToResponses(req.body)
			reqs[i] = req
		}
		sc.requests = reqs
		out = append(out, sc)
	}
	return out
}

const goldenResponsesToolBody = `{"model":"gpt-5","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Weather in Paris?"}]}],` +
	`"tools":[{"type":"function","name":"get_weather","description":"Weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}`

const goldenResponsesHistoryBody = `{"model":"gpt-5","stream":false,"instructions":"Be brief.","max_output_tokens":256,"reasoning":{"effort":"high"},"input":[` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"Weather in Paris?"}]},` +
	`{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"},` +
	`{"type":"function_call_output","call_id":"call_1","output":"sunny"}],` +
	`"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}`

func goldenAnthropicToolStream() []string {
	text := goldenAnthropicStream(goldenAnthropicModel, "")
	return []string{
		text[0],
		goldenSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`),
		goldenSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Paris\"}"}}`),
		text[3],
		strings.Replace(text[4], "end_turn", "tool_use", 1),
		text[5],
	}
}

// withSlowKeepalive enables keepalive pings well inside a paused stream.
func withSlowKeepalive(svc *proxy.Service, _ *goldenHarness) *proxy.Service {
	return svc.WithSSEKeepalive(5 * time.Millisecond)
}

func goldenSlow(step goldenStep) goldenStep {
	step.pause = 60 * time.Millisecond
	return step
}

func goldenResponsesScenarios() []goldenScenario {
	var out []goldenScenario
	for _, stream := range []bool{true, false} {
		out = append(out, goldenAsResponses(goldenMatrixScenarios(ingressOpenAI, stream))...)
		prefix := ingressResponses + "_" + streamName(stream) + "_"
		body := goldenResponsesBody(stream)
		out = append(out,
			goldenScenario{name: prefix + "openai_native_success", ingress: ingressResponses, decisions: []router.Decision{goldenOpenAIDecision},
				upstreams: map[string][]goldenStep{providers.ProviderOpenAI: {responsesOK(stream, "hi")}},
				requests:  []goldenRequest{{body: body}}},
			goldenScenario{name: prefix + "codex_anthropic_success", ingress: ingressResponses, decisions: []router.Decision{goldenAnthropicDecision},
				upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(stream, "hi")}},
				requests:  []goldenRequest{{body: body, headers: goldenCodexHeaders}}},
			goldenScenario{name: prefix + "no_eligible_provider", ingress: ingressResponses, routeErr: router.ErrNoEligibleProvider,
				upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(stream, "never")}},
				requests:  []goldenRequest{{body: body, noTelemetry: true}}},
		)
	}
	out = append(out, goldenAsResponses(goldenStreamScenarios(ingressOpenAI))...)
	out = append(out, goldenAsResponses([]goldenScenario{{
		name: ingressOpenAI + "_nonstream_route_error", ingress: ingressOpenAI, routeErr: errors.New("no eligible model"),
		upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(false, "never")}},
		requests:  []goldenRequest{{body: goldenOpenAIBody(false), noTelemetry: true}},
	}})...)
	native := goldenResponsesStream(goldenOpenAIModel, "partial")
	failed := goldenSSE("response.failed", `{"type":"response.failed","response":{"id":"resp_up","object":"response","status":"failed","model":"`+goldenOpenAIModel+`","output":[],"error":{"code":"server_error","message":"upstream failed"}}}`)
	p := ingressResponses + "_stream_"
	single := func(d router.Decision, provider string, steps ...goldenStep) ([]router.Decision, map[string][]goldenStep) {
		return []router.Decision{d}, map[string][]goldenStep{provider: steps}
	}
	add := func(name, body string, headers map[string]string, d router.Decision, provider string, steps ...goldenStep) {
		decisions, upstreams := single(d, provider, steps...)
		out = append(out, goldenScenario{name: p + name, ingress: ingressResponses, decisions: decisions, upstreams: upstreams,
			requests: []goldenRequest{{body: body, headers: headers}}})
	}
	add("anthropic_tool_call", goldenResponsesToolBody, nil, goldenAnthropicDecision, providers.ProviderAnthropic,
		goldenOK("text/event-stream", goldenAnthropicToolStream()))
	add("openai_native_missing_terminal", goldenResponsesBody(true), nil, goldenOpenAIDecision, providers.ProviderOpenAI,
		goldenStep{status: http.StatusOK, headers: goldenEventStream, frames: native[:3]})
	add("openai_native_failed_event", goldenResponsesBody(true), nil, goldenOpenAIDecision, providers.ProviderOpenAI,
		goldenStep{status: http.StatusOK, headers: goldenEventStream, frames: append(native[:3:3], failed)})
	add("codex_openai_native_missing_terminal", goldenResponsesBody(true), goldenCodexHeaders, goldenOpenAIDecision, providers.ProviderOpenAI,
		goldenStep{status: http.StatusOK, headers: goldenEventStream, frames: native[:3]})
	add("codex_openai_native_success", goldenResponsesBody(true), goldenCodexHeaders, goldenOpenAIDecision, providers.ProviderOpenAI,
		responsesOK(true, "hi"))
	add("compat_empty_stream", goldenResponsesBody(true), nil, goldenCompatDecision, providers.ProviderFireworks,
		goldenStep{status: http.StatusOK, headers: goldenEventStream})
	out = append(out,
		goldenScenario{name: p + "no_keepalive_ping_on_slow_stream", ingress: ingressResponses, configure: withSlowKeepalive,
			decisions: []router.Decision{goldenAnthropicDecision},
			upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {goldenSlow(anthropicOK(true, "slow"))}},
			requests:  []goldenRequest{{body: goldenResponsesBody(true)}}},
		goldenScenario{name: ingressResponses + "_nonstream_history_and_options", ingress: ingressResponses,
			decisions: []router.Decision{goldenAnthropicDecision},
			upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(false, "sunny")}},
			requests:  []goldenRequest{{body: goldenResponsesHistoryBody}}},
		goldenScenario{name: ingressResponses + "_nonstream_invalid_json_body", ingress: ingressResponses,
			upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(false, "never")}},
			requests:  []goldenRequest{{body: `{"model":`, noTelemetry: true}}},
	)
	return out
}

const goldenGeminiHistoryBody = `{"systemInstruction":{"parts":[{"text":"Be brief."}]},` +
	`"generationConfig":{"maxOutputTokens":256,"temperature":0.2},` +
	`"tools":[{"functionDeclarations":[{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}],` +
	`"contents":[{"role":"user","parts":[{"text":"Say hi"}]},` +
	`{"role":"model","parts":[{"text":"✦ **Weave Router** → gemini-2.5-flash · best pick for this turn\n\nhello"}]},` +
	`{"role":"user","parts":[{"text":"Weather in Paris?"}]}]}`

func geminiFinish(stream bool, finish string, part string) goldenStep {
	frame := `{"candidates":[{"content":{"role":"model","parts":[` + part + `]},"index":0,"finishReason":"` + finish + `"}],"modelVersion":"` + goldenGeminiModel + `",` + goldenGeminiUsage + `}`
	if stream {
		return goldenOK("text/event-stream", []string{goldenSSE("", frame)})
	}
	return goldenOK("application/json", []string{frame})
}

func goldenGeminiScenarios() []goldenScenario {
	var out []goldenScenario
	unavailable := goldenUpstreamError(http.StatusServiceUnavailable, `{"error":{"code":503,"message":"unavailable","status":"UNAVAILABLE"}}`)
	limited := goldenUpstreamError(http.StatusTooManyRequests, `{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED"}}`)
	for _, stream := range []bool{true, false} {
		prefix := ingressGemini + "_" + streamName(stream) + "_"
		path := goldenGeminiPath(stream)
		add := func(name, body string, d router.Decision, upstreams map[string][]goldenStep) {
			out = append(out, goldenScenario{name: prefix + name, ingress: ingressGemini, decisions: []router.Decision{d},
				upstreams: upstreams, requests: []goldenRequest{{body: body, path: path}}})
		}
		google := func(steps ...goldenStep) map[string][]goldenStep {
			return map[string][]goldenStep{providers.ProviderGoogle: steps}
		}
		add("google_success", goldenGeminiBody(), goldenGeminiDecision, google(geminiOK(stream, "hi")))
		add("server_error_retries_exhausted", goldenGeminiBody(), goldenGeminiDecision, google(unavailable, unavailable, unavailable))
		add("server_error_retry_success", goldenGeminiBody(), goldenGeminiDecision, google(unavailable, geminiOK(stream, "rescued")))
		add("rate_limited", goldenGeminiBody(), goldenGeminiDecision, google(limited, limited, limited))
		add("rate_limited_retry_success", goldenGeminiBody(), goldenGeminiDecision, google(limited, geminiOK(stream, "rescued")))
		add("timeout", goldenGeminiBody(), goldenGeminiDecision, google(goldenStep{err: &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}}, geminiOK(stream, "rescued")))
		add("upstream_bad_request", goldenGeminiBody(), goldenGeminiDecision, google(goldenUpstreamError(http.StatusBadRequest,
			`{"error":{"code":400,"message":"Invalid value at 'contents'","status":"INVALID_ARGUMENT"}}`)))
		add("upstream_context_overflow", goldenGeminiBody(), goldenGeminiDecision, google(goldenUpstreamError(http.StatusBadRequest,
			`{"error":{"code":400,"message":"The input token count (2000000) exceeds the maximum number of tokens allowed (1048576).","status":"INVALID_ARGUMENT"}}`)))
		add("function_call", goldenGeminiHistoryBody, goldenGeminiDecision, google(geminiFinish(stream, "STOP", `{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}}`)))
		add("max_tokens_finish", goldenGeminiBody(), goldenGeminiDecision, google(geminiFinish(stream, "MAX_TOKENS", `{"text":"trunc"}`)))
		add("history_and_config", goldenGeminiHistoryBody, goldenGeminiDecision, google(geminiOK(stream, "sunny")))
		out = append(out,
			goldenScenario{name: prefix + "cross_format_decision", ingress: ingressGemini, decisions: []router.Decision{goldenAnthropicDecision},
				upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(stream, "never")}},
				requests:  []goldenRequest{{body: goldenGeminiBody(), path: path, noTelemetry: true}}},
			goldenScenario{name: prefix + "no_eligible_provider", ingress: ingressGemini, routeErr: router.ErrNoEligibleProvider,
				upstreams: google(geminiOK(stream, "never")),
				requests:  []goldenRequest{{body: goldenGeminiBody(), path: path, noTelemetry: true}}},
			goldenScenario{name: prefix + "route_error", ingress: ingressGemini, routeErr: errors.New("no eligible model"),
				upstreams: google(geminiOK(stream, "never")),
				requests:  []goldenRequest{{body: goldenGeminiBody(), path: path, noTelemetry: true}}},
		)
	}
	p := ingressGemini + "_stream_"
	frames := goldenGeminiStream("partial")
	stream := func(name string, step goldenStep, rest ...goldenStep) goldenScenario {
		return goldenScenario{name: p + name, ingress: ingressGemini, decisions: []router.Decision{goldenGeminiDecision},
			upstreams: map[string][]goldenStep{providers.ProviderGoogle: append([]goldenStep{step}, rest...)},
			requests:  []goldenRequest{{body: goldenGeminiBody(), path: goldenGeminiPath(true)}}}
	}
	out = append(out,
		stream("cut_after_first_chunk", goldenStep{status: http.StatusOK, headers: goldenEventStream, frames: frames[:1], err: io.ErrUnexpectedEOF}, geminiOK(true, "never")),
		stream("cut_before_first_chunk", goldenStep{status: http.StatusOK, headers: goldenEventStream, err: io.ErrUnexpectedEOF}, geminiOK(true, "rescued")),
		stream("empty_stream", goldenStep{status: http.StatusOK, headers: goldenEventStream}, geminiOK(true, "rescued")),
		stream("missing_finish_reason", goldenStep{status: http.StatusOK, headers: goldenEventStream, frames: frames[:1]}),
		stream("client_cancel_mid_stream", goldenStep{status: http.StatusOK, headers: goldenEventStream, frames: frames[:1], cancel: true}),
	)
	slow := stream("no_keepalive_ping_on_slow_stream", goldenSlow(geminiOK(true, "slow")))
	slow.configure = withSlowKeepalive
	out = append(out, slow)
	for _, bad := range []struct{ name, path, body string }{
		{"invalid_json_body", goldenGeminiPath(false), `{"contents":`},
		{"unknown_action", "/v1beta/models/" + goldenGeminiModel + ":countTokens", goldenGeminiBody()},
		{"missing_model", "/v1beta/models/:generateContent", goldenGeminiBody()},
	} {
		out = append(out, goldenScenario{name: ingressGemini + "_nonstream_" + bad.name, ingress: ingressGemini,
			upstreams: map[string][]goldenStep{providers.ProviderGoogle: {geminiOK(false, "never")}},
			requests:  []goldenRequest{{body: bad.body, path: bad.path, noTelemetry: true}}})
	}
	return out
}

func goldenIngressScenarios() []goldenScenario {
	return append(goldenResponsesScenarios(), goldenGeminiScenarios()...)
}
