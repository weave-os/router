package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

func TestCodexQuotaPreflightRotatesBeforeInference(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		secondHealthy, depleted bool
		wantBearer              string
		quotaFailure            string
	}{
		{"next_subscription", true, false, "Bearer second-seat", ""},
		{"weave_capacity_after_pool", false, false, "Bearer weave-capacity", ""},
		{"depleted_weave_balance", false, true, "", ""},
		{"quota_http_outage", true, false, "Bearer second-seat", "http"},
		{"quota_timeout", true, false, "Bearer second-seat", "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var inferenceBearers []string
			var inferenceMu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				bearer := r.Header.Get("Authorization")
				if r.URL.Path == "/wham/usage" {
					if bearer == "Bearer first-seat" && tc.quotaFailure != "" {
						if tc.quotaFailure == "timeout" {
							<-r.Context().Done()
						} else {
							w.WriteHeader(http.StatusBadGateway)
						}
						return
					}
					if bearer == "Bearer second-seat" && tc.secondHealthy {
						_, _ = io.WriteString(w, `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":10}}}`)
					} else {
						_, _ = io.WriteString(w, `{"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100}},"credits":{"has_credits":true,"balance":"10000"}}`)
					}
					return
				}
				inferenceMu.Lock()
				inferenceBearers = append(inferenceBearers, bearer)
				inferenceMu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"answer\"}\n\n")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
			}))
			defer server.Close()
			client := openai.NewClient("weave-capacity", server.URL)
			client.SetCodexBaseURL(server.URL)
			leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "first", AccessToken: "first-seat", ProviderAccount: "first-provider"}, {AccountID: "second", AccessToken: "second-seat", ProviderAccount: "second-provider"}}}
			svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).WithManagedSubscriptions(leaser).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
			ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
			if tc.depleted {
				ctx = billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyCreditsDepleted)
			}
			body := `{"model":"auto","stream":true,"input":"synthetic","tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]}`
			rec := httptest.NewRecorder()
			err := svc.ProxyOpenAIResponses(ctx, []byte(body), rec, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
			inferenceMu.Lock()
			defer inferenceMu.Unlock()
			if tc.depleted {
				require.Error(t, err)
				require.Empty(t, inferenceBearers)
			} else {
				require.NoError(t, err)
				require.Equal(t, []string{tc.wantBearer}, inferenceBearers)
				require.Contains(t, rec.Body.String(), "answer")
			}
		})
	}
}

func TestModelQuotaRejectionLeavesOtherModelsOnSubscription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wham/usage" {
			_, _ = io.WriteString(w, `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":20}},"additional_rate_limits":[{"normal_model_slug":"gpt-6-astra","rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100}}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"synthetic"}`)
	}))
	defer server.Close()
	client := openai.NewClient("deployment-key", server.URL)
	client.SetCodexBaseURL(server.URL)
	ctx := codexSubscriptionTestCtx()
	credentialCtx := resolveAndInjectCredentials(ctx, providers.ProviderOpenAI, "gpt-6-astra", http.Header{})
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderOpenAI: client}).WithUsageObserver(usage.NewObserver([]byte("synthetic-quota-salt"), time.Minute, time.Now))
	err := client.Proxy(credentialCtx, router.Decision{Model: "gpt-6-astra", Provider: providers.ProviderOpenAI}, providers.PreparedRequest{Endpoint: providers.EndpointResponses, Body: []byte(`{"model":"gpt-6-astra","input":"hi"}`)}, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.Error(t, err)
	_, accountSpent := codexQuotaExhaustion(err)
	require.False(t, accountSpent)
	svc.recordCodexQuotaExhaustion(credentialCtx, http.Header{}, err)
	svc.recordSubscriptionModelRejection(credentialCtx, providers.ProviderOpenAI, "gpt-6-astra", err)
	require.False(t, svc.codexSubscriptionExhausted(credentialCtx, http.Header{}))
	laterCtx := svc.resolveCredentials(ctx, providers.ProviderOpenAI, codexCoveredModel, http.Header{})
	require.True(t, requestcontext.CredentialsFromContext(laterCtx).OAuth)
	require.NoError(t, client.Proxy(laterCtx, router.Decision{Model: codexCoveredModel, Provider: providers.ProviderOpenAI}, providers.PreparedRequest{Endpoint: providers.EndpointResponses, Body: []byte(`{"model":"` + codexCoveredModel + `","input":"hi"}`)}, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil)))
	leaser := &scriptedSubscriptionLeaser{}
	svc.WithManagedSubscriptions(leaser)
	require.False(t, svc.recordManagedSubscriptionFailure(credentialCtx, providers.ProviderOpenAI, "gpt-6-astra", subscriptions.Lease{AccountID: "synthetic-account"}, err), "model quota must not cool down the whole managed account")
}
