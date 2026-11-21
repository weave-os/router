package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
)

func TestProxyMessages_GatewayExhaustionAttributesCortexBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"code":"390503","message":"Service temporarily unavailable."}`)
	}))
	defer upstream.Close()

	svc := proxy.WithStrategyPorts(proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropicGateway, Model: "claude-haiku-4-5"}},
		map[string]providers.Client{providers.ProviderAnthropicGateway: anthropic.NewClient("test-key", upstream.URL)},
		nil, nil, false, providers.ProviderAnthropicGateway, "claude-haiku-4-5", newCaptureTelemetry()), hmm.ServingPorts()).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropicGateway: {}}).
		WithRetrySleep(noRetrySleep)
	ctx := context.WithValue(context.Background(), requestcontext.InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
	ctx = context.WithValue(ctx, requestcontext.ExternalIDContextKey{}, "org-test")
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"claude-haiku-4-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	_ = svc.ProxyMessages(ctx, body, rec, req)

	assert.Equal(t, 503, rec.Code)
	t.Log(rec.Body.String())
	assert.Contains(t, rec.Body.String(), "your gateway (127.0.0.1:")
	assert.Contains(t, rec.Body.String(), ") returned 503 (server error) after 2 router retries: Service temporarily unavailable.")
}
