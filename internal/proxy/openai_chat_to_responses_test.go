package proxy_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// responsesStreamUpstream writes a minimal Responses SSE stream: one text
// delta, one function call, then response.completed with usage.
func responsesStreamUpstream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, frame := range []string{
		`{"type":"response.output_text.delta","output_index":0,"delta":"patching"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`,
		`{"type":"response.function_call_arguments.done","output_index":1,"arguments":"{\"path\":\"main.go\"}"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"main.go\"}"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"main.go\"}"}],"usage":{"input_tokens":11,"output_tokens":7}}}`,
	} {
		_, _ = io.WriteString(w, "data: "+frame+"\n\n")
	}
}

func openAIChatService(provider providers.Client, model string) *proxy.Service {
	return proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: model, Reason: "test"}},
		map[string]providers.Client{providers.ProviderOpenAI: provider},
		nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil,
	)
}

const chatToolTurnBody = `{"model":"auto","stream":true,"max_tokens":2048,
  "messages":[{"role":"user","content":"read main.go"}],
  "tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}],
  "reasoning_effort":"medium"}`

// A chat/completions caller on direct OpenAI is emitted onto /v1/responses and
// translated back, so it never hits the chat/completions 400 that reasoning +
// function tools produces — while still receiving chat/completions on the wire.
func TestService_ProxyOpenAIChatCompletion_TranslatesOntoResponses(t *testing.T) {
	provider := &fakeProvider{proxyResponse: responsesStreamUpstream}
	svc := openAIChatService(provider, "gpt-5.6-luna")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatToolTurnBody))
	require.NoError(t, svc.ProxyOpenAIChatCompletion(context.Background(), []byte(chatToolTurnBody), rec, req))

	require.Len(t, provider.proxyBodies, 1)
	assert.Equal(t, providers.EndpointResponses, provider.proxyEndpoints[0])
	sent := provider.proxyBodies[0]
	assert.Equal(t, "gpt-5.6-luna", gjson.GetBytes(sent, "model").String())
	assert.False(t, gjson.GetBytes(sent, "messages").Exists(), "the upstream request must speak Responses")
	assert.Equal(t, "read main.go", gjson.GetBytes(sent, "input.0.content.0.text").String())
	assert.Equal(t, "read_file", gjson.GetBytes(sent, "tools.0.name").String(),
		"Responses function tools are flat")
	assert.Equal(t, "medium", gjson.GetBytes(sent, "reasoning.effort").String())

	body := rec.Body.String()
	assert.Contains(t, body, `"object":"chat.completion.chunk"`, "the client still speaks chat/completions")
	assert.Contains(t, body, `"content":"patching"`)
	assert.Contains(t, body, `"name":"read_file"`)
	assert.Contains(t, body, `\"path\":\"main.go\"`)
	assert.Contains(t, body, `"finish_reason":"tool_calls"`)
	assert.Contains(t, body, "data: [DONE]")
}

// A non-streaming chat client gets one chat.completion body, not SSE.
func TestService_ProxyOpenAIChatCompletion_TranslatesOntoResponsesNonStreaming(t *testing.T) {
	provider := &fakeProvider{proxyResponse: responsesStreamUpstream}
	svc := openAIChatService(provider, "gpt-5.6-luna")

	body := strings.Replace(chatToolTurnBody, `"stream":true`, `"stream":false`, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	require.NoError(t, svc.ProxyOpenAIChatCompletion(context.Background(), []byte(body), rec, req))

	assert.Equal(t, providers.EndpointResponses, provider.proxyEndpoints[0])
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "data: ")
	out := rec.Body.Bytes()
	assert.Equal(t, "chat.completion", gjson.GetBytes(out, "object").String())
	assert.Equal(t, "read_file", gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.name").String())
	assert.Equal(t, "tool_calls", gjson.GetBytes(out, "choices.0.finish_reason").String())
	assert.EqualValues(t, 11, gjson.GetBytes(out, "usage.prompt_tokens").Int())
}

// Endpoint selection per turn: a chat-only knob keeps the turn on
// chat/completions rather than dropping the field, and killing the broad
// rollout still promotes the reasoning tool turn chat/completions rejects.
func TestService_ProxyOpenAIChatCompletion_ResponsesEndpointSelection(t *testing.T) {
	for _, tc := range []struct {
		name         string
		body         string
		broadOff     bool
		wantEndpoint providers.Endpoint
	}{
		{
			name:         "tool turn goes to Responses",
			body:         chatToolTurnBody,
			wantEndpoint: providers.EndpointResponses,
		},
		{
			name:         "n>1 has no Responses equivalent",
			body:         `{"model":"auto","stream":true,"n":2,"messages":[{"role":"user","content":"hi"}]}`,
			wantEndpoint: providers.EndpointChatCompletions,
		},
		{
			name:         "logprobs has no Responses equivalent",
			body:         `{"model":"auto","stream":true,"logprobs":true,"messages":[{"role":"user","content":"hi"}]}`,
			wantEndpoint: providers.EndpointChatCompletions,
		},
		{
			name:         "rollout off still promotes the reasoning tool turn",
			body:         chatToolTurnBody,
			broadOff:     true,
			wantEndpoint: providers.EndpointResponses,
		},
		{
			name:         "rollout off keeps a plain turn on chat/completions",
			body:         `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			broadOff:     true,
			wantEndpoint: providers.EndpointChatCompletions,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"ok\"}\n\n")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n")
				_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}}
			svc := openAIChatService(provider, "gpt-5.6-luna")

			ctx := context.Background()
			if tc.broadOff {
				ctx = flags.WithOverrides(ctx, flags.Overrides{
					Bools: map[flags.Key]bool{flags.KeyOpenAIResponsesBroad: false},
				})
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tc.body))
			require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(tc.body), rec, req))

			require.Len(t, provider.proxyEndpoints, 1)
			assert.Equal(t, tc.wantEndpoint, provider.proxyEndpoints[0])
		})
	}
}

// An `openai`-named endpoint with no Responses surface falls back to
// chat/completions pre-commit, and the answer is memoized so the next turn
// skips the probe.
func TestService_ProxyOpenAIChatCompletion_FallsBackWhenEndpointLacksResponses(t *testing.T) {
	provider := &fakeProvider{
		proxyErrByEndpoint: map[providers.Endpoint]error{
			providers.EndpointResponses: &providers.UpstreamErrorResponse{
				Status: http.StatusNotFound,
				Body:   []byte(`{"error":{"message":"Unknown path /v1/responses"}}`),
			},
		},
		proxyResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		},
	}
	svc := openAIChatService(provider, "gpt-5.6-luna")

	for _, want := range [][]providers.Endpoint{
		{providers.EndpointResponses, providers.EndpointChatCompletions},
		{providers.EndpointChatCompletions},
	} {
		provider.proxyEndpoints = nil
		provider.proxyBodies = nil
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatToolTurnBody))

		require.NoError(t, svc.ProxyOpenAIChatCompletion(context.Background(), []byte(chatToolTurnBody), rec, req))
		assert.Equal(t, want, provider.proxyEndpoints)
		last := provider.proxyBodies[len(provider.proxyBodies)-1]
		assert.True(t, gjson.GetBytes(last, "messages").Exists(), "the fallback attempt must speak chat/completions")
		assert.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()
		assert.NotContains(t, body, "Unknown path", "the failed Responses probe must not reach the client")
		assert.Contains(t, body, `"content":"ok"`)
	}
}

// Once the translated stream has committed output, an upstream failure may not
// be retried — the client gets the error in-stream on the response it already
// started reading.
func TestService_ProxyOpenAIChatCompletion_NoResponsesFallbackAfterCommit(t *testing.T) {
	// The stream produces output and only then reports the endpoint as
	// unsupported, so the fallback condition is met but the response is not.
	provider := &fakeProvider{
		proxyErr: &providers.UpstreamErrorResponse{
			Status: http.StatusNotFound,
			Body:   []byte(`{"error":{"message":"Unknown path /v1/responses"}}`),
		},
		proxyResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"committed\"}\n\n")
		},
	}
	svc := openAIChatService(provider, "gpt-5.6-luna")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatToolTurnBody))
	require.Error(t, svc.ProxyOpenAIChatCompletion(context.Background(), []byte(chatToolTurnBody), rec, req))

	assert.Equal(t, []providers.Endpoint{providers.EndpointResponses}, provider.proxyEndpoints,
		"a committed response must not be re-dispatched")
	assert.Contains(t, rec.Body.String(), `"content":"committed"`)
}

// A corrupt upstream frame means the turn lost content, so the client must be
// told rather than handed a completion that looks clean.
func TestService_ProxyOpenAIChatCompletion_MalformedUpstreamFrameReported(t *testing.T) {
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"half\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.outp\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	}}
	svc := openAIChatService(provider, "gpt-5.6-luna")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatToolTurnBody))
	require.NoError(t, svc.ProxyOpenAIChatCompletion(context.Background(), []byte(chatToolTurnBody), rec, req))

	body := rec.Body.String()
	assert.Contains(t, body, `"content":"half"`)
	assert.Contains(t, body, "malformed")
	assert.NotContains(t, body, `"finish_reason":"stop"`,
		"a turn that dropped a frame must not report a clean stop")
}

// Tool arguments the model got wrong are a per-model quality signal, so the
// translated chat path must emit the same log line the Anthropic path does.
func TestService_ProxyOpenAIChatCompletion_LogsToolCallIssues(t *testing.T) {
	// Prime observability's sync.Once before overriding slog.Default.
	observability.Get()
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// `path` is declared as a string but emitted as an object, which toolcheck
	// can neither validate nor losslessly coerce.
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, frame := range []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":{\"nested\":1}}"}}`,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`,
		} {
			_, _ = io.WriteString(w, "data: "+frame+"\n\n")
		}
	}}
	svc := openAIChatService(provider, "gpt-5.6-luna")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatToolTurnBody))
	require.NoError(t, svc.ProxyOpenAIChatCompletion(context.Background(), []byte(chatToolTurnBody), rec, req))

	assert.Contains(t, logBuf.String(), "router.tool_call_invalid")
	assert.Contains(t, logBuf.String(), "read_file")
}

// A gateway provider keeps its own narrow rule: a plain chat turn must not be
// promoted onto a Responses surface most gateways don't mount.
func TestService_ProxyOpenAIChatCompletion_GatewayKeepsChatCompletions(t *testing.T) {
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}}
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAIGateway, Model: "gpt-5.6-luna", Reason: "test"}},
		map[string]providers.Client{providers.ProviderOpenAIGateway: provider},
		nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil,
	)

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	require.NoError(t, svc.ProxyOpenAIChatCompletion(context.Background(), []byte(body), rec, req))

	require.Len(t, provider.proxyEndpoints, 1)
	assert.Equal(t, providers.EndpointChatCompletions, provider.proxyEndpoints[0])
}
