package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	earlyStopLowModel  = "claude-haiku-4-5"
	earlyStopHighModel = "claude-opus-4-7"
)

func anthropicSSE(model string, blocks []string, stopReason string) string {
	out := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"" + model + "\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n"
	for _, b := range blocks {
		out += b
	}
	out += "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"" + stopReason + "\"},\"usage\":{\"output_tokens\":250}}\n\n"
	out += "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	return out
}

func textBlock(text string) string {
	return "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + text + "\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
}

func toolUseBlock(index, id string) string {
	return "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":" + index + ",\"content_block\":{\"type\":\"tool_use\",\"id\":\"" + id + "\",\"name\":\"Bash\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":" + index + ",\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"command\\\":\\\"ls\\\"}\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":" + index + "}\n\n"
}

const earlyStopToolResultBody = `{"model":"claude-opus-4-7","stream":true,"max_tokens":4096,
"tools":[{"name":"Bash","description":"run","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],
"messages":[
{"role":"user","content":"fix the three failing tests"},
{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./..."}}]},
{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"FAIL a\nFAIL b\nFAIL c"}]}]}`

type earlyStopFixture struct {
	svc      *proxy.Service
	mu       sync.Mutex
	calls    map[string]int
	low      func(w http.ResponseWriter)
	high     func(w http.ResponseWriter)
	upstream *httptest.Server
}

func newEarlyStopFixture(t *testing.T, mode flags.EarlyStopRedoMode) *earlyStopFixture {
	t.Helper()
	f := &earlyStopFixture{calls: map[string]int{}}
	f.low = func(w http.ResponseWriter) {
		writeSSE(w, anthropicSSE(earlyStopLowModel, []string{textBlock("Fixed test a. Next I will fix b and c.")}, "end_turn"))
	}
	f.high = func(w http.ResponseWriter) {
		writeSSE(w, anthropicSSE(earlyStopHighModel, []string{textBlock("Fixing b."), toolUseBlock("1", "toolu_2")}, "tool_use"))
	}
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		model := gjson.GetBytes(body, "model").String()
		f.mu.Lock()
		f.calls[model]++
		f.mu.Unlock()
		if model == earlyStopHighModel {
			f.high(w)
			return
		}
		f.low(w)
	}))
	t.Cleanup(f.upstream.Close)
	decision := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    earlyStopLowModel,
		Metadata: &router.RoutingMetadata{
			PolicyGroup:     "cluster-3",
			CandidateModels: []string{earlyStopLowModel, earlyStopHighModel},
			CandidateProviders: map[string]string{
				earlyStopLowModel:  providers.ProviderAnthropic,
				earlyStopHighModel: providers.ProviderAnthropic,
			},
		},
	}
	f.svc = proxy.NewService(
		&fakeRouter{decision: decision},
		map[string]providers.Client{providers.ProviderAnthropic: anthropic.NewClient("test-anthropic-key", f.upstream.URL)},
		nil, false, nil, newFakePinStore(), false, providers.ProviderAnthropic, "claude-haiku-4-5", newCaptureTelemetry(),
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}}).
		WithRetrySleep(noRetrySleep).
		WithEarlyStopRedo(mode)
	return f
}

func writeSSE(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func (f *earlyStopFixture) serve(t *testing.T, clientApp string) *httptest.ResponseRecorder {
	t.Helper()
	ctx := requestcontext.WithClientIdentity(authedCtx("11111111-1111-1111-1111-111111111111"), proxy.ClientIdentity{ClientApp: clientApp})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, f.svc.ProxyMessages(ctx, []byte(earlyStopToolResultBody), rec, req))
	return rec
}

func (f *earlyStopFixture) callsTo(model string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[model]
}

func TestProxyMessages_EarlyStopRedo_ReservesStoppedToolResultTurnOnTopTier(t *testing.T) {
	f := newEarlyStopFixture(t, flags.EarlyStopRedoOn)

	rec := f.serve(t, proxy.ClientAppClaudeCode)

	assert.Equal(t, 1, f.callsTo(earlyStopLowModel))
	assert.Equal(t, 1, f.callsTo(earlyStopHighModel), "the stopped turn is re-served once on the top-tier candidate")
	out := rec.Body.String()
	assert.NotContains(t, out, "Next I will fix b and c", "the client never sees the stopped response")
	assert.Contains(t, out, `"type":"tool_use"`, "the client gets the re-served turn's tool call")
	assert.Equal(t, 1, strings.Count(out, "event: message_start"), "one assistant message reaches the client")
	assert.Equal(t, earlyStopHighModel, rec.Header().Get(proxy.HeaderRouterModel))
}

func TestProxyMessages_EarlyStopRedo_ShadowServesStoppedResponse(t *testing.T) {
	f := newEarlyStopFixture(t, flags.EarlyStopRedoShadow)

	rec := f.serve(t, proxy.ClientAppClaudeCode)

	assert.Zero(t, f.callsTo(earlyStopHighModel), "shadow mode never re-serves")
	assert.Contains(t, rec.Body.String(), "Next I will fix b and c")
	assert.Equal(t, earlyStopLowModel, rec.Header().Get(proxy.HeaderRouterModel))
}

func TestProxyMessages_EarlyStopRedo_ToolCallPassesThrough(t *testing.T) {
	f := newEarlyStopFixture(t, flags.EarlyStopRedoOn)
	f.low = func(w http.ResponseWriter) {
		writeSSE(w, anthropicSSE(earlyStopLowModel, []string{textBlock("Running b."), toolUseBlock("1", "toolu_3")}, "tool_use"))
	}

	rec := f.serve(t, proxy.ClientAppClaudeCode)

	assert.Zero(t, f.callsTo(earlyStopHighModel), "a turn that calls a tool is served as is")
	out := rec.Body.String()
	assert.Contains(t, out, "Running b.")
	assert.Contains(t, out, "toolu_3")
	assert.Contains(t, out, "event: message_stop")
}

func TestProxyMessages_EarlyStopRedo_FailedRedoServesStoppedResponse(t *testing.T) {
	f := newEarlyStopFixture(t, flags.EarlyStopRedoOn)
	f.high = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`))
	}

	rec := f.serve(t, proxy.ClientAppClaudeCode)

	assert.Equal(t, 1, f.callsTo(earlyStopHighModel))
	assert.Equal(t, http.StatusOK, rec.Code)
	out := rec.Body.String()
	assert.Contains(t, out, "Next I will fix b and c", "a failed redo falls back to the stopped response")
	assert.Contains(t, out, "event: message_stop")
	assert.NotContains(t, out, "invalid_request_error")
	assert.Equal(t, earlyStopLowModel, rec.Header().Get(proxy.HeaderRouterModel))
}

func TestProxyMessages_EarlyStopRedo_OnlyClaudeCode(t *testing.T) {
	f := newEarlyStopFixture(t, flags.EarlyStopRedoOn)

	rec := f.serve(t, proxy.ClientAppCodex)

	assert.Zero(t, f.callsTo(earlyStopHighModel))
	assert.Contains(t, rec.Body.String(), "Next I will fix b and c")
}

func TestProxyMessages_EarlyStopRedo_OrgOverrideTurnsItOn(t *testing.T) {
	f := newEarlyStopFixture(t, flags.EarlyStopRedoOff)
	ctx := flags.WithOverrides(
		requestcontext.WithClientIdentity(authedCtx("11111111-1111-1111-1111-111111111111"), proxy.ClientIdentity{ClientApp: proxy.ClientAppClaudeCode}),
		flags.Overrides{Strings: map[flags.Key]string{flags.KeyEarlyStopRedo: string(flags.EarlyStopRedoOn)}})
	rec := httptest.NewRecorder()
	require.NoError(t, f.svc.ProxyMessages(ctx, []byte(earlyStopToolResultBody), rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))

	assert.Equal(t, 1, f.callsTo(earlyStopHighModel))
	assert.NotContains(t, rec.Body.String(), "Next I will fix b and c")
}
