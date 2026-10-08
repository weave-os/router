package proxy_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestProxyOpenAIResponses_ForcedOpusPlaintextAgentTask(t *testing.T) {
	var upstreamBodies [][]byte
	var mu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "cannot read request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		upstreamBodies = append(upstreamBodies, body)
		mu.Unlock()
		messages := gjson.GetBytes(body, "messages").Array()
		if len(messages) == 0 || messages[len(messages)-1].Get("role").String() != string(translate.EscalationRoleUser) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"This model does not support assistant message prefill. The conversation must end with a user message."}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if len(gjson.GetBytes(body, "tools").Array()) > 0 {
			name := gjson.GetBytes(body, "tools.0.name").Str
			_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_parent\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":10}}}\n\n"+
				fmt.Sprintf("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_task\",\"name\":%q,\"input\":{}}}\n\n", name)+
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"message\\\":\\\"Inspect the project tests.\\\"}\"}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":10}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		_, _ = io.WriteString(w, strings.NewReplacer("rescued", "task accepted", "claude-sonnet-5", "claude-opus-5-5").Replace(anthropicRescueSSE))
	}))
	t.Cleanup(upstream.Close)
	svc := proxy.NewService(&fakeRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropic, Model: "claude-opus-5-5", Reason: translate.ReasonUserForceModel,
	}}, map[string]providers.Client{
		providers.ProviderAnthropic: anthropic.NewClient("test-key", upstream.URL),
	}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-opus-5-5", nil)
	ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex})
	parentBody := []byte(`{"model":"gpt-6.1-sol","stream":true,"input":"Delegate project test inspection","tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object","properties":{"message":{"type":"string"}}}}]}]}`)
	parentSink := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIResponses(ctx, parentBody, parentSink, httptest.NewRequest(http.MethodPost, "/v1/responses", nil)))
	var call gjson.Result
	for _, line := range strings.Split(parentSink.Body.String(), "\n") {
		if strings.HasPrefix(line, "data: ") && gjson.Get(line[6:], "type").Str == "response.output_item.done" && gjson.Get(line[6:], "item.type").Str == "function_call" {
			call = gjson.Get(line[6:], "item")
		}
	}
	require.Equal(t, "spawn_agent", call.Get("name").Str)
	require.Equal(t, "collaboration", call.Get("namespace").Str)
	require.Equal(t, "[]", call.Get("encrypted_function_args").Raw, "Codex's plaintext task-delivery signal must be on the executable done item")
	// Codex uses the empty marker to deliver these arguments as plaintext.
	task := gjson.Get(call.Get("arguments").Str, "message").Str
	delivery, err := json.Marshal("Message Type: NEW_TASK\nTask name: child\nSender: parent\nPayload:\n" + task)
	require.NoError(t, err)
	body := []byte(`{"model":"gpt-6.1-sol","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Inspect the project"}]},{"type":"agent_message","author":"parent","recipient":"child","content":[{"type":"input_text","text":` + string(delivery) + `}]}]}`)
	sink := httptest.NewRecorder()
	err = svc.ProxyOpenAIResponses(ctx, body, sink, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))

	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, upstreamBodies, 2, "one parent attempt and one child attempt on the explicitly selected model")
	assert.Equal(t, "claude-opus-5-5", gjson.GetBytes(upstreamBodies[1], "model").String())
	assert.Equal(t, "Message Type: NEW_TASK\nTask name: child\nSender: parent\nPayload:\nInspect the project tests.", gjson.GetBytes(upstreamBodies[1], "messages.1.content.0.text").String())
	assert.Equal(t, string(translate.EscalationRoleUser), gjson.GetBytes(upstreamBodies[1], "messages.1.role").String())
	assert.NotContains(t, string(upstreamBodies[1]), "Continue.")
	assert.Contains(t, sink.Body.String(), "task accepted")
	assert.Contains(t, sink.Body.String(), "response.completed")
	assert.NotContains(t, sink.Body.String(), "response.failed")
}

func TestProxyOpenAIResponses_EncryptedAgentTaskKeepsNativePayload(t *testing.T) {
	native := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_native","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Task received"}]}]}`)
	}}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-6.1-sol"}}
	svc := proxy.NewService(fr, map[string]providers.Client{
		providers.ProviderOpenAI: native, providers.ProviderAnthropic: &fakeProvider{},
	}, nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-6.1-sol", nil)
	ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex})
	body := []byte(`{"model":"gpt-6.1-sol","input":[{"type":"agent_message","author":"parent","recipient":"child","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"opaque-task"}]}]}`)
	sink := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, sink, httptest.NewRequest(http.MethodPost, "/v1/responses", nil)))
	require.NotNil(t, fr.capturedReq)
	assert.Equal(t, map[string]struct{}{providers.ProviderOpenAI: {}}, fr.capturedReq.EnabledProviders)
	require.Len(t, native.proxyBodies, 1)
	assert.JSONEq(t, string(body), string(native.proxyBodies[0]))
	assert.Equal(t, providers.EndpointResponses, native.proxyEndpoints[0])
	blockedUpstream := &fakeProvider{}
	blockedService := proxy.NewService(&fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-5-5"}}, map[string]providers.Client{
		providers.ProviderAnthropic: blockedUpstream,
	}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-opus-5-5", nil)
	err := blockedService.ProxyOpenAIResponses(ctx, body, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	assert.ErrorIs(t, err, proxy.ErrTranslationCompatibleProviderUnavailable)
	assert.Empty(t, blockedUpstream.proxyBodies, "never substitute the clear routing header for an opaque task")
}
