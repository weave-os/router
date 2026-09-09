package proxy_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
)

func TestRecoveryLoadAndClassifierRestoration(t *testing.T) {
	if os.Getenv("ROUTER_RECOVERY_LOAD") == "" {
		t.Skip("set ROUTER_RECOVERY_LOAD for the 20,000-request fault lane")
	}
	for _, concurrency := range []int{32, 128} {
		t.Run(fmt.Sprint(concurrency), func(t *testing.T) {
			var healthy atomic.Bool
			var now atomic.Int64
			now.Store(time.Now().UnixNano())
			var activeRequests, maxActiveRequests atomic.Int64
			sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				current := activeRequests.Add(1)
				defer activeRequests.Add(-1)
				for old := maxActiveRequests.Load(); current > old && !maxActiveRequests.CompareAndSwap(old, current); old = maxActiveRequests.Load() {
				}
				if !healthy.Load() {
					w.WriteHeader(503)
					return
				}
				var request struct {
					Candidates []struct {
						RosterID string `json:"roster_id"`
					} `json:"candidates"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Candidates) == 0 {
					w.WriteHeader(400)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"schema_version": "policy_router_v1", "selected_roster_id": request.Candidates[0].RosterID})
			}))
			defer sidecar.Close()
			availableProviders := map[string]struct{}{providers.ProviderAnthropic: {}}
			var servedRequests atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				servedRequests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"Recovered answer"}],"stop_reason":"end_turn","usage":{"input_tokens":8,"output_tokens":3}}`)
			}))
			defer upstream.Close()
			client := policyclient.New(sidecar.URL, sidecar.Client(), time.Second, policyclient.WithResilience(policyclient.ResilienceConfig{MaxConcurrent: 16, FailureThreshold: 1, Now: func() time.Time { return time.Unix(0, now.Load()) }}))
			service := proxy.NewService(nil, map[string]providers.Client{providers.ProviderAnthropic: anthropic.NewClient("synthetic-key", upstream.URL)}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
				WithHMMRouter(hmm.New(client, availableProviders)).WithAvailableModels(map[string]struct{}{"claude-haiku-4-5": {}}).WithDeploymentKeyedProviders(availableProviders)
			ctx := observability.WithLogger(router.WithStrategy(context.Background(), router.StrategyHMM), slog.New(slog.NewTextHandler(io.Discard, nil)))
			request := router.Request{RequestedModel: "claude-sonnet-4-6", ConversationMessages: []router.ConversationMessage{{Role: "user", Text: "Synthetic recovery task"}}}
			body := []byte(`{"model":"claude-opus-4-8","max_tokens":2048,"messages":[{"role":"user","content":"Synthetic recovery task"}]}`)
			jobs := make(chan struct{})
			var workers sync.WaitGroup
			var requestFailures atomic.Int64
			for i := 0; i < concurrency; i++ {
				workers.Add(1)
				go func() {
					defer workers.Done()
					for range jobs {
						response := httptest.NewRecorder()
						err := service.ProxyMessages(ctx, body, response, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
						if err != nil || response.Header().Get(proxy.HeaderRouterModel) != "claude-haiku-4-5" || !strings.Contains(response.Body.String(), "Recovered answer") {
							requestFailures.Add(1)
						}
					}
				}()
			}
			for i := 0; i < 10000; i++ {
				jobs <- struct{}{}
			}
			close(jobs)
			workers.Wait()
			assert.Zero(t, requestFailures.Load())
			assert.Equal(t, int64(10000), servedRequests.Load())
			assert.LessOrEqual(t, maxActiveRequests.Load(), int64(16))
			healthy.Store(true)
			now.Add(int64(time.Minute))
			decision, err := service.Route(ctx, request)
			require.NoError(t, err)
			assert.Nil(t, decision.Recovery)
			require.NotNil(t, decision.Metadata)
			assert.Equal(t, string(router.StrategyHMM), decision.Metadata.Strategy)
		})
	}
}
