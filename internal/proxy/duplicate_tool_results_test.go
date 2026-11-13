package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

// codexNotifyTurnBody is a Codex code-mode history where an exec script
// called notify() three times, so its call_id carries four outputs.
const codexNotifyTurnBody = `{"model":"gpt-5.6-sol","input":[` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"poll the build"}]},` +
	`{"type":"custom_tool_call","call_id":"toolu_dup_1","name":"exec","input":"notify(status)"},` +
	`{"type":"custom_tool_call_output","call_id":"toolu_dup_1","output":[{"type":"input_text","text":"Script completed\nOutput:\n"},{"type":"input_text","text":"build passed"}]},` +
	`{"type":"custom_tool_call_output","call_id":"toolu_dup_1","output":"status: queued"},` +
	`{"type":"custom_tool_call_output","call_id":"toolu_dup_1","output":"status: running"},` +
	`{"type":"custom_tool_call_output","call_id":"toolu_dup_1","output":"status: passed"},` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"what next?"}]}` +
	`],"tools":[{"type":"custom","name":"exec"}]}`

func codexClientCtx() context.Context {
	return context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex})
}

func TestProxyOpenAIResponses_CodexDuplicateToolOutputsReachAnthropicOnce(t *testing.T) {
	anthropic := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-5", Reason: "test"}}
	svc := proxy.NewService(fr, map[string]providers.Client{
		providers.ProviderAnthropic: anthropic,
		providers.ProviderOpenAI:    &fakeProvider{},
	}, nil, nil, false, providers.ProviderAnthropic, "claude-opus-5", nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIResponses(codexClientCtx(), []byte(codexNotifyTurnBody), rec, req))

	require.Len(t, anthropic.proxyBodies, 1)
	var results []gjson.Result
	gjson.GetBytes(anthropic.proxyBodies[0], "messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "tool_result" {
				results = append(results, block)
			}
			return true
		})
		return true
	})
	require.Len(t, results, 1, "Anthropic 400s on a tool_use with more than one tool_result")
	assert.Contains(t, results[0].Get("content").Raw, "status: passed")
}

func TestProxyOpenAIResponses_CodexDuplicateToolOutputsStayVerbatimOnNativeOpenAI(t *testing.T) {
	openai := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]}`)
	}}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-sol", Reason: "test"}}
	svc := proxy.NewService(fr, map[string]providers.Client{
		providers.ProviderOpenAI: openai,
	}, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIResponses(codexClientCtx(), []byte(codexNotifyTurnBody), rec, req))

	require.Len(t, openai.proxyBodies, 1)
	assert.Equal(t, providers.EndpointResponses, openai.proxyEndpoints[0])
	outputs := 0
	gjson.GetBytes(openai.proxyBodies[0], "input").ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "custom_tool_call_output" && item.Get("call_id").String() == "toolu_dup_1" {
			outputs++
		}
		return true
	})
	assert.Equal(t, 4, outputs, "native Responses dispatch forwards the caller's own history")
}

func TestProxyMessages_DuplicateToolResultsReachAnthropicOnce(t *testing.T) {
	anthropic := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-5", Reason: "test"}}
	svc := proxy.NewService(fr, map[string]providers.Client{
		providers.ProviderAnthropic: anthropic,
	}, nil, nil, false, providers.ProviderAnthropic, "claude-opus-5", nil)

	body := []byte(`{"model":"claude-opus-5","max_tokens":512,"messages":[` +
		`{"role":"user","content":"poll the build"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_dup_1","name":"exec","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_dup_1","content":"done"},{"type":"tool_result","tool_use_id":"toolu_dup_1","content":"progress"},{"type":"text","text":"what next?"}]}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(context.Background(), body, rec, req))

	require.Len(t, anthropic.proxyBodies, 1)
	results := 0
	gjson.GetBytes(anthropic.proxyBodies[0], "messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("tool_use_id").String() == "toolu_dup_1" {
				results++
			}
			return true
		})
		return true
	})
	assert.Equal(t, 1, results)
}
