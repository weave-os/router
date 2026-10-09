package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

func TestSubscriptionOnlySlowFirstOutputDoesNotUsePaidCapacity(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSyntheticCodexQuota(w, r) {
			return
		}
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer synthetic-slow-seat" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		select {
		case <-time.After(11 * time.Second):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"slow subscription answer\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
	}))
	defer upstream.Close()
	client := openai.NewClient("synthetic-paid-key", upstream.URL)
	client.SetCodexBaseURL(upstream.URL)
	svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).
		WithManagedSubscriptions(&scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "synthetic-slow-account", AccessToken: "synthetic-slow-seat", ProviderAccount: "synthetic-provider"}}}).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
	ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
	ctx = billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyCreditsDepleted)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"synthetic slow prefill"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`
	rec := httptest.NewRecorder()
	err := svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), "slow subscription answer")
	require.EqualValues(t, 1, calls.Load(), "subscription-only requests must not use the available paid key")
}
