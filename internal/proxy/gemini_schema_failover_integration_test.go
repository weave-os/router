package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/providers/google"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// PrepareGemini fails before Google is dialed; the turn must be rescued on Anthropic.
func TestProxyMessages_GeminiBuildIncompatibilityFailsOverToBaselineAnthropic(t *testing.T) {
	var (
		mu                     sync.Mutex
		googleCount            int
		anthropicCount         int
		anthropicReceivedModel string
	)

	googleUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		googleCount++
		mu.Unlock()
		t.Error("Google upstream must not be hit: PrepareGemini fails before any binding is dispatched")
		w.WriteHeader(http.StatusOK)
	}))
	defer googleUpstream.Close()

	anthropicUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		anthropicCount++
		anthropicReceivedModel = gjson.GetBytes(body, "model").String()
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(anthropicMessageSSE))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer anthropicUpstream.Close()

	store := newFakePinStore()
	tel := newCaptureTelemetry()
	svc := proxy.NewService(
		// Router routes the turn to a Gemini 3.x model; the unsigned tool
		// history below makes PrepareGemini fail before Google is ever dialed.
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderGoogle, Model: "gemini-3-pro-preview"}},
		map[string]providers.Client{
			providers.ProviderGoogle:    google.NewNativeClient("test-google-key", googleUpstream.URL),
			providers.ProviderAnthropic: anthropic.NewClient("test-anthropic-key", anthropicUpstream.URL),
		},
		nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", tel,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderGoogle:    {},
		providers.ProviderAnthropic: {},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	// The tool_use id carries no "__thought__" signature, so
	// HasUnsignedToolCallHistory is true and PrepareGemini deterministically
	// fails with ErrGeminiUnsignedToolHistory against a Gemini 3.x target.
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"messages":[` +
		`{"role":"user","content":"please continue"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}` +
		`]}`)

	err := svc.ProxyMessages(authedCtx("11111111-1111-1111-1111-111111111111"), body, rec, req)
	require.NoError(t, err, "ProxyMessages should succeed via baseline failover to Anthropic despite the build-time Gemini incompatibility")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 0, googleCount, "Google must never be dispatched: PrepareGemini fails before any binding is attempted")
	assert.Equal(t, 1, anthropicCount, "Anthropic baseline failover dispatched once")
	assert.Equal(t, "claude-opus-4-8", anthropicReceivedModel, "baseline failover must request the caller's model on Anthropic")

	assert.Equal(t, http.StatusOK, rec.Code, "the client must see a successful response, not a 502")
	respBody := rec.Body.String()
	assert.Contains(t, respBody, "event: message_start", "client sees the Anthropic stream start")
	assert.Contains(t, respBody, "event: message_stop", "client sees the Anthropic stream end")
	assert.Contains(t, respBody, "hi", "client sees the canned completion text")
	assert.Equal(t, providers.ProviderAnthropic, rec.Header().Get(proxy.HeaderRouterProvider), "served provider header reflects the baseline failover")
	assert.Equal(t, "claude-opus-4-8", rec.Header().Get(proxy.HeaderRouterModel), "x-router-model reflects the baseline model that served")
}
