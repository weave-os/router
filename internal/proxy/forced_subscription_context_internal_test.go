package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/translate"
)

func TestForcedSubscriptionContextEstimatePreservesHardExclusions(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		forced     bool
		estimate   bool
		unsigned   bool
		wantServed bool
	}{
		{name: "forced estimate delegates to provider", forced: true, estimate: true, wantServed: true},
		{name: "automatic estimate still excludes", estimate: true},
		{name: "explicit policy exclusion still excludes", forced: true},
		{name: "unsigned history still excludes", forced: true, estimate: true, unsigned: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			request := router.Request{
				ExcludedModels:       modelSet([]string{gatewayCompactionOpusModel}),
				SafetyExcludedModels: modelSet([]string{gatewayCompactionOpusModel}),
			}
			if scenario.forced {
				request.ForceModel = gatewayCompactionOpusModel
			}
			if scenario.estimate {
				request.ContextWindowExcludedModels = modelSet([]string{gatewayCompactionOpusModel})
			}
			if scenario.unsigned {
				request.UnsignedHistoryExcludedModels = modelSet([]string{gatewayCompactionOpusModel})
			}
			_, narrowed := (&Service{}).subscriptionStateRequest(context.Background(), request, []string{gatewayCompactionOpusModel})
			_, allowed := narrowed.AllowedModels[gatewayCompactionOpusModel]
			assert.Equal(t, scenario.wantServed, allowed)
			assert.Contains(t, request.ExcludedModels, gatewayCompactionOpusModel, "state routing must not mutate the original request")
			assert.Contains(t, request.SafetyExcludedModels, gatewayCompactionOpusModel)
		})
	}
}

func TestForcedSubscriptionContextEstimateReachesAnthropic(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		transient bool
		overflow  bool
		direct    bool
	}{
		{name: "estimate exceeds window but provider accepts"},
		{name: "direct subscription survives estimated overflow", direct: true},
		{name: "transient provider failure retries same model", transient: true},
		{name: "real overflow allows compaction and continuation", overflow: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var models, credentials, apiKeys, contents []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				models = append(models, gjson.GetBytes(body, "model").String())
				credentials = append(credentials, r.Header.Get("Authorization"))
				apiKeys = append(apiKeys, r.Header.Get("x-api-key"))
				content := gjson.GetBytes(body, "messages.0.content")
				if content.IsArray() {
					var text strings.Builder
					for _, block := range content.Array() {
						text.WriteString(block.Get("text").String())
					}
					contents = append(contents, text.String())
				} else {
					contents = append(contents, content.String())
				}
				w.Header().Set("Content-Type", "application/json")
				if scenario.transient && len(models) == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"temporarily unavailable"}}`)
					return
				}
				if scenario.overflow && len(models) <= 2 {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1100000 tokens > 1000000 maximum"}}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"synthetic","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"synthetic answer"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`)
			}))
			defer upstream.Close()
			store := &forcedPinStore{pin: sessionpin.Pin{
				Provider: providers.ProviderAnthropic, Model: gatewayCompactionOpusModel,
				Reason: translate.ReasonUserForceModel, PinnedUntil: pinNeverExpires,
			}}
			svc := NewService(nil, map[string]providers.Client{
				providers.ProviderAnthropic: anthropic.NewClient("synthetic-paid-key", upstream.URL),
				providers.ProviderOpenAI:    planClient{&fakeClient{name: providers.ProviderOpenAI}},
			}, nil, false, nil, store, false, "", "", nil).
				WithManagedSubscriptions(&stateModelLeaser{activeProviders: map[subscriptions.Provider]bool{subscriptions.ProviderClaude: true}}).
				WithAvailableModels(modelSet([]string{gatewayCompactionOpusModel, catalog.ModelGPT6Luna})).
				WithDeploymentKeyedProviders(modelSet([]string{providers.ProviderAnthropic, providers.ProviderOpenAI})).
				WithCompactionHardPin(true).
				WithRetrySleep(func(context.Context, time.Duration) error { return nil })
			ctx := context.WithValue(context.Background(), APIKeyIDContextKey{}, "synthetic-key")
			ctx = context.WithValue(ctx, InstallationIDContextKey{}, uuid.NewString())
			ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppClaudeCode, SessionID: "synthetic-session"})
			ctx = context.WithValue(ctx, ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}})
			ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{gatewayCompactionOpusModel})
			ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{gatewayCompactionOpusModel})
			if scenario.direct {
				ctx = context.WithValue(ctx, AnthropicSubscriptionContextKey{}, "sk-ant-oat01-synthetic-token")
			}

			// The body estimate plus output reserve exceeds Opus's window while
			// a larger-window model remains routable, as in a long tool history.
			largeHistory := strings.Repeat("x", 3_600_000)
			proxyTurn := func(history, system string) (*httptest.ResponseRecorder, error) {
				body, err := json.Marshal(map[string]any{
					"model": gatewayCompactionOpusModel, "max_tokens": 128_000,
					"system": system, "messages": []map[string]string{{"role": "user", "content": history}},
				})
				require.NoError(t, err)
				recorder := httptest.NewRecorder()
				err = svc.ProxyMessages(WithManagedSubscriptionUsage(ctx), body, recorder, httptest.NewRequest(http.MethodPost, routePathMessages, strings.NewReader(string(body))))
				return recorder, err
			}

			recorder, err := proxyTurn(largeHistory, "synthetic coding task")
			if scenario.overflow {
				require.Error(t, err)
				class, ok := ClassifyDispatchError(err)
				require.True(t, ok)
				require.Equal(t, DispatchErrorContextWindowExceeded, class.Kind)
				require.Len(t, models, 1, "a real overflow must reach the client without retrying the oversized turn")
				recorder, err = proxyTurn(largeHistory, "Your task is to create a detailed summary of the conversation so far.")
				require.Error(t, err)
				class, ok = ClassifyDispatchError(err)
				require.True(t, ok)
				require.Equal(t, http.StatusBadRequest, class.Status)
				require.Regexp(t, claudeCodePromptTooLong, class.Message, "Claude Code must receive the counts it needs to trim compaction history")
				recorder, err = proxyTurn("trimmed synthetic history", "Your task is to create a detailed summary of the conversation so far.")
				require.NoError(t, err)
				require.Contains(t, recorder.Body.String(), "synthetic answer")
				recorder, err = proxyTurn("compacted synthetic history; continue the task", "synthetic coding task")
			}
			require.NoError(t, err)
			require.Contains(t, recorder.Body.String(), "synthetic answer")
			wantAttempts := 1
			if scenario.transient {
				wantAttempts = 2
			}
			if scenario.overflow {
				wantAttempts = 4
			}
			require.Len(t, models, wantAttempts)
			for i, model := range models {
				assert.Equal(t, gatewayCompactionOpusModel, model)
				if scenario.transient && i == 1 {
					assert.Equal(t, "synthetic-paid-key", apiKeys[i], "an actual subscription 503 permits funded recovery on the same model")
					continue
				}
				wantCredential := "Bearer synthetic-token"
				if scenario.direct {
					wantCredential = "Bearer sk-ant-oat01-synthetic-token"
				}
				assert.Equal(t, wantCredential, credentials[i], "context estimation must not force a paid attempt")
			}
			assert.True(t, largeHistory == contents[0], "the router must let the provider count the original history (sent %d bytes)", len(contents[0]))
		})
	}
}
