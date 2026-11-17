package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

func newCommandOrderingService(fp *fakeProvider) *proxy.Service {
	return proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-4-6", Reason: "test"}},
		map[string]providers.Client{providers.ProviderAnthropic: fp},
		nil,
		newFakePinStore(),
		false,
		providers.ProviderAnthropic,
		"claude-haiku-4-5",
		nil,
	)
}

func TestProxyMessages_ForceModelAfterOrphanedToolUseStaysAgentIssued(t *testing.T) {
	body := `{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[` +
		`{"role":"user","content":"start the task"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_skill","name":"Skill","input":{}}]},` +
		`{"role":"user","content":"/force-model opus"}]}`
	fp := &fakeProvider{}
	svc := newCommandOrderingService(fp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", strings.NewReader(""))

	require.NoError(t, svc.ProxyMessages(authedCtx(uuid.New().String()), []byte(body), rec, req))

	require.Len(t, fp.proxyBodies, 1, "a command after an assistant tool_use is agent-issued and must continue upstream, not answer synthetically")
}

func TestProxyOpenAIChatCompletion_ForceModelAfterOrphanedToolCallStaysAgentIssued(t *testing.T) {
	body := `{"model":"claude-sonnet-4-6","messages":[` +
		`{"role":"user","content":"start the task"},` +
		`{"role":"assistant","tool_calls":[{"id":"call_skill","type":"function","function":{"name":"Skill","arguments":"{}"}}]},` +
		`{"role":"user","content":"/force-model opus"}]}`
	fp := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}}
	svc := newCommandOrderingService(fp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/chat/completions", strings.NewReader(""))

	require.NoError(t, svc.ProxyOpenAIChatCompletion(authedCtx(uuid.New().String()), []byte(body), rec, req))

	require.Len(t, fp.proxyBodies, 1, "a command after an assistant tool call is agent-issued and must continue upstream, not answer synthetically")
}
