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

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
)

func TestGeminiToolModeRetrySkippedOnceOutputCommitted(t *testing.T) {
	const model = "gemini-3.1-pro-preview"
	client := &sequencedClient{responses: []func(http.ResponseWriter) error{
		func(w http.ResponseWriter) error {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"parts":[{"text":"partial answer"}],"role":"model"},"index":0}]}`+"\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return &providers.UpstreamStatusError{Status: http.StatusBadRequest}
		},
		func(w http.ResponseWriter) error {
			w.WriteHeader(http.StatusOK)
			return nil
		},
	}}
	svc := proxy.WithStrategyPorts(proxy.NewService(&fakeRouter{decision: router.Decision{Provider: providers.ProviderGoogle, Model: model}},
		dispatch.NewClients(map[string]providers.Client{providers.ProviderGoogle: client}), nil, nil, false, providers.ProviderGoogle, model, nil), hmm.ServingPorts()).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderGoogle: {}})
	body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"synthetic task"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}`
	rec := httptest.NewRecorder()

	_ = svc.ProxyOpenAIChatCompletion(context.Background(), []byte(body), rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

	require.Len(t, client.bodies, 1, "a downgrade retry after committed output would replay the turn")
	assert.Contains(t, rec.Body.String(), "partial answer")
}

func TestProxyOpenAIResponses_CyberRefusalRetryNeverLandsOnOpenAI(t *testing.T) {
	upstreams := &cyberRefusalUpstreams{openAIResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"cyber_policy","message":"This content was flagged for possible cybersecurity risk."}}`)
	}}
	openAIURL, anthropicURL := upstreams.start(t)
	svc := cyberRefusalService(openAIURL, anthropicURL, "test", newFakePinStore(), newCaptureTelemetry()).
		WithCyberRefusalRetry(true).
		WithCyberRefusalFallbackModel("gpt-5.5")

	rec := proxyResponsesTurn(t, svc)

	openAIHits, anthropicHits := upstreams.counts()
	assert.Equal(t, 1, openAIHits, "the refusing vendor's classifier would decline the retry too")
	assert.Zero(t, anthropicHits)
	assert.Contains(t, rec.Body.String(), "cybersecurity risk")
}
