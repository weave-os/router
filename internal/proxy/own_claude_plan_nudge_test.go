package proxy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

// A spent own Claude plan moves the turn onto the deployment key; the marker
// tells the user so they can connect a seat instead of running on credits.
func TestProxyMessages_SpentOwnClaudePlanNudgesToConnectASeat(t *testing.T) {
	var upstreamAuth []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAuth = append(upstreamAuth, r.Header.Get("Authorization"))
		writeSSE(w, anthropicSSE(earlyStopLowModel, []string{textBlock("hi")}, "end_turn"))
	}))
	t.Cleanup(upstream.Close)
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: earlyStopLowModel, Reason: "test"}},
		map[string]providers.Client{providers.ProviderAnthropic: anthropic.NewClient("test-anthropic-key", upstream.URL)},
		nil, newFakePinStore(), false, providers.ProviderAnthropic, earlyStopLowModel, newCaptureTelemetry(),
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}}).WithRetrySleep(noRetrySleep).WithUsageObserver(exhaustedObserver())
	body := `{"model":"claude-opus-4-7","max_tokens":4096,"stream":true,"messages":[{"role":"user","content":"hello"}]}`

	serve := func(body string) string {
		rec := httptest.NewRecorder()
		ctx := context.WithValue(authedCtx("11111111-1111-1111-1111-111111111111"), proxy.AnthropicSubscriptionContextKey{}, bypassSubToken)
		require.NoError(t, svc.ProxyMessages(claudeCreditFallbackContext(ctx), []byte(body), rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))
		return rec.Body.String()
	}

	first := serve(body)
	require.Contains(t, first, "your own Claude plan is out of usage")
	require.Contains(t, first, proxy.SubscriptionsDashboardURL+"?reconnect=claude", "the link starts the Claude login")
	require.NotContains(t, upstreamAuth[0], bypassSubToken, "the spent plan is not forwarded")

	echoed, err := json.Marshal(map[string]any{"model": "claude-opus-4-7", "max_tokens": 4096, "stream": true, "messages": []map[string]any{
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": []map[string]string{{"type": "text", "text": "✦ **Weave Router** → your own Claude plan is out of usage for now. Connect or reconnect a Claude seat to keep this on a subscription: " + proxy.SubscriptionsDashboardURL + "?reconnect=claude\n\nhi"}}},
		{"role": "user", "content": "again"},
	}})
	require.NoError(t, err)
	require.NotContains(t, serve(string(echoed)), "your own Claude plan is out of usage", "shown once per session")
}
