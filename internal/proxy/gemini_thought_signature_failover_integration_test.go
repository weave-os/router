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

// A signed tool history passes the unsigned-history gate, so Gemini 3.x is
// dialed — and 400s "Corrupted thought signature." (prod 2026-09-08: a
// client-mangled `__thought__` tool id). The router cannot repair an opaque
// signature and an identical re-POST 400s forever, so the turn must be
// rescued on the Anthropic baseline instead of surfacing the 400 and bricking
// every later turn of the session.
func TestProxyMessages_GeminiCorruptedThoughtSignatureFailsOverToBaselineAnthropic(t *testing.T) {
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Corrupted thought signature.","status":"INVALID_ARGUMENT"}}`))
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
	// The tool id carries a decodable `__thought__` payload, so the history
	// counts as signed and PrepareGemini lets the turn reach Google.
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"messages":[` +
		`{"role":"user","content":"please continue"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"call_f5f7e3f9_82a725763982__thought__RXFZS0Nx","name":"Bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_f5f7e3f9_82a725763982__thought__RXFZS0Nx","content":"ok"}]}` +
		`]}`)

	err := svc.ProxyMessages(authedCtx("11111111-1111-1111-1111-111111111111"), body, rec, req)
	require.NoError(t, err, "ProxyMessages should succeed via baseline failover to Anthropic after Gemini rejects the signature")

	mu.Lock()
	defer mu.Unlock()
	assert.GreaterOrEqual(t, googleCount, 1, "Google is dialed: the history looks signed")
	assert.Equal(t, 1, anthropicCount, "Anthropic baseline failover dispatched once")
	assert.Equal(t, "claude-opus-4-8", anthropicReceivedModel, "baseline failover must request the caller's model on Anthropic")

	assert.Equal(t, http.StatusOK, rec.Code, "the client must see a successful response, not the Gemini 400")
	respBody := rec.Body.String()
	assert.Contains(t, respBody, "event: message_start")
	assert.Contains(t, respBody, "event: message_stop")
	assert.NotContains(t, respBody, "Corrupted thought signature")
	assert.Equal(t, providers.ProviderAnthropic, rec.Header().Get(proxy.HeaderRouterProvider))
	assert.Equal(t, "claude-opus-4-8", rec.Header().Get(proxy.HeaderRouterModel))
}
