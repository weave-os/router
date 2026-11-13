package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router/sessionpin"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const rescuedSessionID = "session-pin-cross-model-rescue"

type providerRescuePinStore struct {
	*fakePinStore
}

func newProviderRescuePinStore() *providerRescuePinStore {
	store := newFakePinStore()
	store.persistUpserts = true
	return &providerRescuePinStore{fakePinStore: store}
}

func (s *providerRescuePinStore) UpdateUsage(_ context.Context, key [sessionpin.SessionKeyLen]byte, role string, usage sessionpin.Usage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usages = append(s.usages, usage)
	if !s.hasPin || s.pin.SessionKey != key || s.pin.Role != role || s.pin.Strategy != usage.Strategy {
		return nil
	}
	if !s.pin.LastTurnEndedAt.IsZero() && usage.EndedAt.Before(s.pin.LastTurnEndedAt) {
		return nil
	}
	// Mirrors UpdateSessionPinUsage: a cross-model rescue preserves the pinned binding.
	if s.pin.Model == "" || usage.ServedModel == s.pin.Model || strings.HasPrefix(usage.ServedModel, s.pin.Model+":") {
		s.pin.Provider = usage.ServedProvider
	}
	s.pin.LastServedModel = usage.ServedModel
	s.pin.LastTurnEndedAt = usage.EndedAt
	return nil
}

func TestProxyMessages_CrossModelRescueKeepsNextTurnOnPinnedBinding(t *testing.T) {
	var mu sync.Mutex
	var anthropicModels, fireworksModels []string
	anthropicServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		model := gjson.GetBytes(body, "model").String()
		mu.Lock()
		anthropicModels = append(anthropicModels, model)
		attempt := len(anthropicModels)
		mu.Unlock()
		if attempt <= dispatch.MaxSameBindingRetries+1 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(statusOverloaded)
			_, _ = io.WriteString(w, overloadedSSE)
			return
		}
		writeAnthropicSuccess(w)
	}))
	t.Cleanup(anthropicServer.Close)

	fireworksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		model := gjson.GetBytes(body, "model").String()
		mu.Lock()
		fireworksModels = append(fireworksModels, model)
		mu.Unlock()
		writeOpenAISuccess(w, model)
	}))
	t.Cleanup(fireworksServer.Close)

	store := newProviderRescuePinStore()
	svc := proxy.NewService(
		&fakeRouter{decision: siblingClusterDecision("")},
		map[string]providers.Client{
			providers.ProviderAnthropic: anthropic.NewClient("test-anthropic-key", anthropicServer.URL),
			providers.ProviderFireworks: openaicompat.NewClient("test-fw-key", fireworksServer.URL),
		},
		nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", newCaptureTelemetry(),
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderAnthropic: {}, providers.ProviderFireworks: {},
	}).WithRetrySleep(noRetrySleep).WithRescuedFailureArmDemotion(false)

	first := runRescueTurn(t, svc, []byte(`{"model":"claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"start this session"}]}`))
	require.Equal(t, providers.ProviderFireworks, first.Header().Get(proxy.HeaderRouterProvider))
	require.Equal(t, "deepseek/deepseek-v4-pro", first.Header().Get(proxy.HeaderRouterModel))
	store.mu.Lock()
	require.NotEmpty(t, store.usages, "the rescued turn must write usage to its pin")
	assert.Equal(t, providers.ProviderFireworks, store.usages[len(store.usages)-1].ServedProvider)
	store.mu.Unlock()

	second := runRescueTurn(t, svc, []byte(`{"model":"claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"start this session"},{"role":"assistant","content":"first answer"},{"role":"user","content":"continue with a new prompt"}]}`))
	mu.Lock()
	gotAnthropic, gotFireworks := append([]string(nil), anthropicModels...), append([]string(nil), fireworksModels...)
	mu.Unlock()
	assert.Equal(t, []string{"claude-opus-4-8"}, gotAnthropic[dispatch.MaxSameBindingRetries+1:],
		"turn 2 must return to Anthropic with its pinned model")
	assert.Equal(t, []string{"deepseek/deepseek-v4-pro"}, gotFireworks,
		"the rescuer must not receive the pinned model on turn 2; dispatches were Anthropic %v, Fireworks %v",
		gotAnthropic, gotFireworks)
	assert.Equal(t, providers.ProviderAnthropic, second.Header().Get(proxy.HeaderRouterProvider))
	assert.Equal(t, "claude-opus-4-8", second.Header().Get(proxy.HeaderRouterModel))
}

func runRescueTurn(t *testing.T, svc *proxy.Service, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	req.Header.Set("X-Claude-Code-Session-Id", rescuedSessionID)
	ctx := context.WithValue(
		authedCtx("11111111-1111-1111-1111-111111111111"),
		proxy.ClientIdentityContextKey{},
		proxy.ClientIdentity{SessionID: rescuedSessionID},
	)
	require.NoError(t, svc.ProxyMessages(ctx, body, recorder, req))
	return recorder
}

func writeAnthropicSuccess(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n")
	_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"answer\"}}\n\n")
	_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n")
	_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
}

func writeOpenAISuccess(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, `data: {"id":"fw-1","object":"chat.completion.chunk","created":1,"model":"`+model+`","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`+"\n\n")
	_, _ = io.WriteString(w, `data: {"id":"fw-1","object":"chat.completion.chunk","created":1,"model":"`+model+`","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`+"\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

var _ sessionpin.Store = (*providerRescuePinStore)(nil)
