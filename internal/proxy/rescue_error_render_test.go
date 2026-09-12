package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProxyMessages_SubscriptionRescueFailureRendersSSEErrorFrame: the routing
// marker is already on the wire when the subscription rescue fails, so the
// error has to arrive as an in-stream frame — a JSON envelope appended to a
// live SSE stream is unparseable to the client.
func TestProxyMessages_SubscriptionRescueFailureRendersSSEErrorFrame(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"quota exhausted"}}`))
	}))
	defer upstream.Close()

	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{
			Provider: providers.ProviderAnthropic,
			Model:    "claude-opus-4-8",
			Metadata: &router.RoutingMetadata{CandidateModels: []string{"claude-opus-4-8"}},
		}},
		map[string]providers.Client{
			providers.ProviderAnthropic: anthropic.NewClient("test-anthropic-key", upstream.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}}).
		WithRetrySleep(noRetrySleep)

	ctx := context.WithValue(
		authedCtx("11111111-1111-1111-1111-111111111111"),
		proxy.AnthropicSubscriptionContextKey{},
		"sk-ant-oat01-subscription-token",
	)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(ctx, body, rec, req)
	require.Error(t, err, "every attempt failed, so the turn surfaces the upstream error")

	respBody := rec.Body.String()
	require.Contains(t, respBody, "event: message_start",
		"the prelude must already be client-visible for this to exercise the rescue renderer")
	assert.Contains(t, respBody, "event: error\ndata: ",
		"the rescue's failure must be framed as an SSE error event")
	assert.NotContains(t, respBody, "\n\n{\"type\":\"error\"",
		"a bare JSON envelope must never follow the stream's frames")
	assert.Equal(t, 1, strings.Count(respBody, "rate_limit_error"),
		"the upstream error reaches the client exactly once")
}
