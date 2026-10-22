package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProxyMessages_SubscriptionRescueFailurePreservesHTTPError verifies that
// a synthetic marker never turns pre-output upstream exhaustion into HTTP 200.
func TestProxyMessages_SubscriptionRescueFailurePreservesHTTPError(t *testing.T) {
	var subscriptionAttempts, paidAttempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), "sk-ant-oat01-") {
			subscriptionAttempts.Add(1)
			w.Header().Set("anthropic-ratelimit-unified-5h-utilization", "1")
			w.Header().Set("anthropic-ratelimit-unified-5h-reset", "4102444800")
		} else {
			paidAttempts.Add(1)
		}
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
		WithRetrySleep(noRetrySleep).
		WithUsageObserver(usage.NewObserver([]byte("rescue-render-quota-salt"), time.Hour, time.Now))

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
	assert.Equal(t, int32(dispatch.MaxSameBindingRetries+1), subscriptionAttempts.Load(), "direct Claude retains its bounded same-binding retries")
	assert.Positive(t, paidAttempts.Load(), "confirmed exhaustion authorizes the failed API rescue under test")

	respBody := rec.Body.String()
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.NotContains(t, respBody, "event: message_start")
	assert.NotContains(t, respBody, "✦ **Weave Router**")
	assert.Equal(t, 1, strings.Count(respBody, "rate_limit_error"),
		"the upstream error reaches the client exactly once")
}
