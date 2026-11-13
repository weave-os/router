package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
)

// A caller whose own ChatGPT plan is spent must not be re-preflighted on every
// turn: the first rejection is remembered until the plan resets.
func TestCodexOwnPlanSpentIsRememberedAcrossTurns(t *testing.T) {
	var mu sync.Mutex
	callerPreflights := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := r.Header.Get("Authorization")
		if r.URL.Path == "/wham/usage" {
			if bearer == "Bearer "+codexTestToken {
				mu.Lock()
				callerPreflights++
				mu.Unlock()
				_, _ = io.WriteString(w, `{"plan_type":"team","rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100,"reset_at":`+
					strconv.FormatInt(time.Now().Add(3*time.Hour).Unix(), 10)+`}}}`)
				return
			}
			_, _ = io.WriteString(w, `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":10}}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"answer\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
	}))
	defer server.Close()
	client := openai.NewClient("weave-capacity", server.URL)
	client.SetCodexBaseURL(server.URL)
	svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}}).
		WithUsageObserver(usage.NewObserver([]byte("test-salt"), time.Hour, time.Now))

	for turn := 0; turn < 2; turn++ {
		ctx := WithManagedSubscriptionUsage(context.WithValue(context.Background(), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111"))
		body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"synthetic"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`
		rec := httptest.NewRecorder()
		require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, codexSubHTTPRequest("/v1/chat/completions", body)))
		require.Contains(t, rec.Body.String(), "answer")
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, callerPreflights, "the spent plan must be suppressed on the second turn")
}
