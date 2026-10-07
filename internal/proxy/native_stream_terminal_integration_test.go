package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	nativeChatModel      = "deepseek/deepseek-v4-flash"
	nativeResponsesModel = "gpt-5.6-luna"
)

var (
	anthropicCutFrames = []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"a\"}}\n\n",
	}
	anthropicCompleteFrames = append(append([]string{}, anthropicCutFrames...),
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")

	chatCutFrames = []string{
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"},"finish_reason":null}]}` + "\n\n",
	}
	chatCompleteFrames = append(append([]string{}, chatCutFrames...),
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`+"\n\n",
		"data: [DONE]\n\n")

	responsesCutFrames = []string{
		`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"},"sequence_number":0}` + "\n\n",
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"partial","sequence_number":1}` + "\n\n",
	}
	responsesCompleteFrames = append(append([]string{}, responsesCutFrames...),
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}],"usage":{"input_tokens":5,"output_tokens":1}},"sequence_number":2}`+"\n\n")
)

// scriptedStream answers each dispatch with the next frame list, writing a
// 200 event stream that ends cleanly at the transport level however much of
// the protocol it carried.
func scriptedStream(attempts ...[]string) *fakeProvider {
	attemptIndex := 0
	return &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		frames := attempts[min(attemptIndex, len(attempts)-1)]
		attemptIndex++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, frame := range frames {
			_, _ = io.WriteString(w, frame)
		}
	}}
}

func nativeSurfaceService(provider *fakeProvider, providerName, model string) *proxy.Service {
	return proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providerName, Model: model, Reason: "test"}},
		map[string]providers.Client{providerName: provider},
		nil, false, nil, nil, false, providerName, model, nil,
	).WithOpenAIResponsesBroad(providerName == providers.ProviderOpenAI).
		WithRetrySleep(noRetrySleep)
}

// The adapter returns nil on a clean EOF after a 200, so a connection the
// upstream closed mid-turn used to be recorded as a served turn.
func TestProxyMessages_NativeStreamWithoutMessageStopFailsInStream(t *testing.T) {
	logBuf := captureCompletionLog(t)
	provider := scriptedStream(anthropicCutFrames)
	svc := makeProxyService(
		router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8", Reason: "test"},
		map[string]providers.Client{providers.ProviderAnthropic: provider},
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(streamCutTurnBody))
	require.Error(t, svc.ProxyMessages(context.Background(), []byte(streamCutTurnBody), rec, req))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "event: error\ndata: ", "the client must see the turn fail, not just stop")
	assert.NotContains(t, rec.Body.String(), "message_stop")
	assert.Len(t, provider.proxyBodies, 1, "a committed stream is never re-dispatched")
	assert.Contains(t, logBuf.String(), "stream_failure_class=upstream_incomplete")
}

func TestProxyMessages_EmptyNativeStreamRetriedBeforeCommit(t *testing.T) {
	provider := scriptedStream(nil, anthropicCompleteFrames)
	svc := makeProxyService(
		router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8", Reason: "test"},
		map[string]providers.Client{providers.ProviderAnthropic: provider},
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(streamCutTurnBody))
	require.NoError(t, svc.ProxyMessages(context.Background(), []byte(streamCutTurnBody), rec, req))

	assert.Len(t, provider.proxyBodies, 2, "an empty 200 put nothing on the wire, so it is retried")
	assert.Contains(t, rec.Body.String(), "message_stop")
	assert.NotContains(t, rec.Body.String(), "event: error")
}

func TestProxyOpenAIChatCompletion_NativeStreamWithoutFinishReasonFailsInStream(t *testing.T) {
	provider := scriptedStream(chatCutFrames)
	svc := nativeSurfaceService(provider, providers.ProviderOpenRouter, nativeChatModel)
	body := []byte(`{"model":"` + nativeChatModel + `","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	rec := httptest.NewRecorder()
	err := svc.ProxyOpenAIChatCompletion(authedCtx(uuid.NewString()), body, rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))

	require.Error(t, err)
	require.Equal(t, []providers.Endpoint{providers.EndpointChatCompletions}, provider.proxyEndpoints)
	assert.Contains(t, rec.Body.String(), "partial")
	assert.Contains(t, rec.Body.String(), `data: {"error":`, "the client must see the turn fail, not just stop")
}

func TestProxyOpenAIChatCompletion_EmptyNativeStreamRetriedBeforeCommit(t *testing.T) {
	provider := scriptedStream(nil, chatCompleteFrames)
	svc := nativeSurfaceService(provider, providers.ProviderOpenRouter, nativeChatModel)
	body := []byte(`{"model":"` + nativeChatModel + `","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIChatCompletion(authedCtx(uuid.NewString()), body, rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)))

	assert.Len(t, provider.proxyBodies, 2)
	assert.Contains(t, rec.Body.String(), `"finish_reason":"stop"`)
	assert.NotContains(t, rec.Body.String(), `{"error":`)
}

func TestProxyOpenAIResponses_NativeStreamWithoutTerminalFailsInStream(t *testing.T) {
	provider := scriptedStream(responsesCutFrames)
	svc := nativeSurfaceService(provider, providers.ProviderOpenAI, nativeResponsesModel)
	body := []byte(`{"model":"auto","stream":true,"input":"hi"}`)

	rec := httptest.NewRecorder()
	err := svc.ProxyOpenAIResponses(authedCtx(uuid.NewString()), body, rec, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))

	require.Error(t, err)
	require.Equal(t, []providers.Endpoint{providers.EndpointResponses}, provider.proxyEndpoints)
	assert.Contains(t, rec.Body.String(), "partial")
	assert.Contains(t, rec.Body.String(), "response.failed", "a Responses client needs a terminal event to stop waiting")
}

func TestProxyOpenAIResponses_EmptyNativeStreamRetriedBeforeCommit(t *testing.T) {
	provider := scriptedStream(nil, responsesCompleteFrames)
	svc := nativeSurfaceService(provider, providers.ProviderOpenAI, nativeResponsesModel)
	body := []byte(`{"model":"auto","stream":true,"input":"hi"}`)

	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIResponses(authedCtx(uuid.NewString()), body, rec, httptest.NewRequest(http.MethodPost, "/v1/responses", nil)))

	assert.Equal(t, []providers.Endpoint{providers.EndpointResponses, providers.EndpointResponses}, provider.proxyEndpoints)
	assert.Contains(t, rec.Body.String(), "response.completed")
	assert.NotContains(t, rec.Body.String(), "response.failed")
}
