package proxy

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

func TestVerificationNonstreamFailedJSONCannotWin(t *testing.T) {
	fixtures := []struct {
		name, path, body string
		run              func(*Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{"responses", "/v1/responses", `{"model":"auto","stream":false,"input":"synthetic","tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]}`, (*Service).ProxyOpenAIResponses},
		{"messages", "/v1/messages", `{"model":"auto","stream":false,"max_tokens":100,"messages":[{"role":"user","content":"synthetic"}],"tools":[{"name":"read_file","input_schema":{"type":"object"}}]}`, (*Service).ProxyMessages},
		{"chat", "/v1/chat/completions", `{"model":"auto","stream":false,"messages":[{"role":"user","content":"synthetic"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`, (*Service).ProxyOpenAIChatCompletion},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			var bearers, paths, providerAccounts []string
			var capturesMu sync.Mutex
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturesMu.Lock()
				bearers = append(bearers, r.Header.Get("Authorization"))
				paths = append(paths, r.URL.Path)
				providerAccounts = append(providerAccounts, r.Header.Get("ChatGPT-Account-ID"))
				capturesMu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"synthetic-failure","object":"response","status":"failed","error":{"code":"usage_limit_reached","message":"synthetic included exhaustion"},"output":[],"usage":{"input_tokens":11,"output_tokens":0}}`)
			}))
			defer upstream.Close()
			client := &includedOnlySyntheticClient{Client: openai.NewClient("synthetic-api-key", upstream.URL)}
			client.SetCodexBaseURL(upstream.URL)
			leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "first-account", OwnerID: "first-owner", Tier: auth.SubscriptionTierPersonal, AccessToken: "first-seat", ProviderAccount: "first-provider"}, {AccountID: "second-account", OwnerID: "second-owner", Tier: auth.SubscriptionTierShared, AccessToken: "second-seat"}}}
			svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).WithManagedSubscriptions(leaser).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
			ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
			rec := httptest.NewRecorder()
			err := fixture.run(svc, ctx, []byte(fixture.body), rec, httptest.NewRequest(http.MethodPost, fixture.path, strings.NewReader(fixture.body)))
			assert.Error(t, err, "failed provider JSON must not become successful request")
			capturesMu.Lock()
			defer capturesMu.Unlock()
			require.GreaterOrEqual(t, len(bearers), 3, "uncommitted failed JSON rotates personal, shared, then API")
			require.Equal(t, []string{"Bearer first-seat", "Bearer second-seat"}, bearers[:2])
			for _, bearer := range bearers[2:] {
				require.Equal(t, "Bearer synthetic-api-key", bearer)
			}
			require.Equal(t, "/responses", paths[0], "the personal subscription attempt uses the Codex Responses endpoint")
			require.Equal(t, "first-provider", providerAccounts[0], "the personal lease keeps its provider account identity")
			winner := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
			assert.False(t, winner.Served, "failure must not count as an included winner")
			require.False(t, winner.OverageInUse)
			require.NotContains(t, rec.Body.String(), `"status":"completed"`)
			require.NotContains(t, rec.Body.String(), `"finish_reason":"stop"`)
		})
	}
}
