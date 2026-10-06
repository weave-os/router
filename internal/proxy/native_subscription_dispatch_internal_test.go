package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

// Exercise the production adapter rather than a fixture that advertises a
// billing guarantee the real provider does not expose.
func TestNativeCodexSubscriptionWithDepletedCredits(t *testing.T) {
	for _, managed := range []bool{false, true} {
		name := "direct"
		if managed {
			name = "managed"
		}
		t.Run(name, func(t *testing.T) {
			var authorizations, accountIDs, paths []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveSyntheticCodexQuota(w, r) {
					return
				}
				authorizations = append(authorizations, r.Header.Get("Authorization"))
				accountIDs = append(accountIDs, r.Header.Get("ChatGPT-Account-ID"))
				paths = append(paths, r.URL.Path)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"subscription answer\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
			}))
			defer upstream.Close()
			client := openai.NewClient("synthetic-paid-key", upstream.URL)
			client.SetCodexBaseURL(upstream.URL)
			svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).
				WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
			ctx := codexSubscriptionTestCtx()
			expectedBearer := "Bearer " + codexTestToken
			if managed {
				ctx = context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
				svc.WithManagedSubscriptions(&scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "synthetic-seat", AccessToken: "synthetic-linked-token", ProviderAccount: codexTestAccountID}}})
				expectedBearer = "Bearer synthetic-linked-token"
			}
			ctx = WithManagedSubscriptionUsage(billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyCreditsDepleted))
			body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"inspect synthetic input"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`
			recorder := httptest.NewRecorder()
			err := svc.ProxyOpenAIChatCompletion(ctx, []byte(body), recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
			require.NoError(t, err)
			require.Equal(t, []string{expectedBearer}, authorizations)
			require.Equal(t, []string{codexTestAccountID}, accountIDs)
			require.Equal(t, []string{"/responses"}, paths)
			require.Contains(t, recorder.Body.String(), "subscription answer")
			require.True(t, svc.costNeutralSubscriptionServed(ctx))
		})
	}
}
