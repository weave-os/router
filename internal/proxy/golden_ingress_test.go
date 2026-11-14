package proxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

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

	goldenOpenAIModel    = "gpt-5.5"
	goldenCodexUserAgent = "codex_cli_rs/0.50.0"
)

type goldenIngressSurface struct {
	path  string
	serve func(svc *proxy.Service, ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) error
}

var goldenIngressSurfaces = map[string]goldenIngressSurface{
	ingressAnthropic: {path: "/v1/messages", serve: (*proxy.Service).ProxyMessages},
	ingressOpenAI:    {path: "/v1/chat/completions", serve: (*proxy.Service).ProxyOpenAIChatCompletion},
	ingressResponses: {path: "/v1/responses", serve: goldenHandler("/v1/responses", openaiapi.ResponsesHandler)},
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

func goldenResponsesBody(stream bool) string {
	return `{"model":"gpt-5","stream":` + strconv.FormatBool(stream) + `,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Say hi"}]}]}`
}

func goldenResponsesStream(model, text string) []string {
	item := `{"type":"message","id":"msg-upstream","status":"completed","role":"assistant","content":[{"type":"output_text","text":"` + text + `","annotations":[]}]}`
	return []string{
		goldenSSE("response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"resp-upstream","object":"response","status":"in_progress","model":"`+model+`","output":[]}}`),
		goldenSSE("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"message","id":"msg-upstream","status":"in_progress","role":"assistant","content":[]}}`),
		goldenSSE("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":2,"item_id":"msg-upstream","output_index":0,"content_index":0,"delta":"`+text+`"}`),
		goldenSSE("response.output_item.done", `{"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":`+item+`}`),
		goldenSSE("response.completed", `{"type":"response.completed","sequence_number":4,"response":{"id":"resp-upstream","object":"response","status":"completed","model":"`+model+`","output":[`+item+`],"usage":{"input_tokens":12,"output_tokens":7,"total_tokens":19}}}`),
	}
}

func responsesOK(stream bool, text string) goldenStep {
	if stream {
		return goldenOK("text/event-stream", goldenResponsesStream(goldenOpenAIModel, text))
	}
	return goldenOK("application/json", []string{`{"id":"resp-upstream","object":"response","status":"completed","model":"` + goldenOpenAIModel + `","output":[{"type":"message","id":"msg-upstream","status":"completed","role":"assistant","content":[{"type":"output_text","text":"` + text + `","annotations":[]}]}],"usage":{"input_tokens":12,"output_tokens":7,"total_tokens":19}}`})
}

var (
	goldenOpenAIDecision = decision(providers.ProviderOpenAI, goldenOpenAIModel)
	goldenEventStream    = map[string]string{"Content-Type": "text/event-stream"}
	goldenCodexHeaders   = map[string]string{"User-Agent": goldenCodexUserAgent}
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
	frames := goldenAnthropicStream(goldenAnthropicModel, "")
	return []string{
		frames[0],
		goldenSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`),
		goldenSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Paris\"}"}}`),
		frames[3],
		strings.Replace(frames[4], "end_turn", "tool_use", 1),
		frames[5],
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
	nativeFrames := goldenResponsesStream(goldenOpenAIModel, "partial")
	failed := goldenSSE("response.failed", `{"type":"response.failed","sequence_number":3,"response":{"id":"resp-upstream","object":"response","status":"failed","model":"`+goldenOpenAIModel+`","output":[],"error":{"code":"server_error","message":"upstream failed"}}}`)
	streamPrefix := ingressResponses + "_stream_"
	add := func(name, body string, headers map[string]string, d router.Decision, provider string, steps ...goldenStep) {
		out = append(out, goldenScenario{name: streamPrefix + name, ingress: ingressResponses, decisions: []router.Decision{d},
			upstreams: map[string][]goldenStep{provider: steps}, requests: []goldenRequest{{body: body, headers: headers}}})
	}
	add("anthropic_tool_call", goldenResponsesToolBody, nil, goldenAnthropicDecision, providers.ProviderAnthropic,
		goldenOK("text/event-stream", goldenAnthropicToolStream()))
	add("openai_native_missing_terminal", goldenResponsesBody(true), nil, goldenOpenAIDecision, providers.ProviderOpenAI,
		goldenStep{status: http.StatusOK, headers: goldenEventStream, frames: nativeFrames[:3]})
	add("openai_native_failed_event", goldenResponsesBody(true), nil, goldenOpenAIDecision, providers.ProviderOpenAI,
		goldenStep{status: http.StatusOK, headers: goldenEventStream, frames: append(nativeFrames[:3:3], failed)})
	add("codex_openai_native_missing_terminal", goldenResponsesBody(true), goldenCodexHeaders, goldenOpenAIDecision, providers.ProviderOpenAI,
		goldenStep{status: http.StatusOK, headers: goldenEventStream, frames: nativeFrames[:3]})
	add("codex_openai_native_success", goldenResponsesBody(true), goldenCodexHeaders, goldenOpenAIDecision, providers.ProviderOpenAI,
		responsesOK(true, "hi"))
	add("compat_empty_stream", goldenResponsesBody(true), nil, goldenCompatDecision, providers.ProviderFireworks,
		goldenStep{status: http.StatusOK, headers: goldenEventStream})
	out = append(out,
		goldenScenario{name: streamPrefix + "no_keepalive_ping_on_slow_stream", ingress: ingressResponses, configure: withSlowKeepalive,
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

func goldenIngressScenarios() []goldenScenario {
	return goldenResponsesScenarios()
}
