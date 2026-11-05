package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

type cyclingSubscriptionLeaser struct {
	scriptedSubscriptionLeaser
	calls int
}

func (c *cyclingSubscriptionLeaser) Lease(_ context.Context, _ auth.SubscriptionOwner, _ subscriptions.Provider, _ string) (subscriptions.Lease, bool, error) {
	lease := c.leases[c.calls%len(c.leases)]
	c.calls++
	return lease, true, nil
}

// Accounts the provider just answered "overloaded" on are skipped by the next
// request instead of being walked again from the top.
func TestManagedPoolOverloadIsRememberedAcrossRequests(t *testing.T) {
	var mu sync.Mutex
	poolHits := map[int]int{}
	turn := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := r.Header.Get("Authorization")
		if r.URL.Path == "/wham/usage" {
			_, _ = io.WriteString(w, `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":10}}}`)
			return
		}
		if strings.HasSuffix(bearer, "-seat") {
			mu.Lock()
			poolHits[turn]++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":{"message":"Our servers are currently overloaded. Please try again later.","type":"server_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"answer\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
	}))
	defer server.Close()
	client := openai.NewClient("weave-capacity", server.URL)
	client.SetCodexBaseURL(server.URL)
	leaser := &cyclingSubscriptionLeaser{scriptedSubscriptionLeaser: scriptedSubscriptionLeaser{leases: []subscriptions.Lease{
		{AccountID: "first", AccessToken: "first-seat", ProviderAccount: "first-provider"},
		{AccountID: "second", AccessToken: "second-seat", ProviderAccount: "second-provider"},
	}}}
	svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).
		WithManagedSubscriptions(leaser).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
	svc.retrySleep = noopSleep

	for turn = 0; turn < 2; turn++ {
		ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
		ctx = WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{
			{ID: "first", Provider: auth.SubscriptionProviderCodex, State: auth.SubscriptionAccountStateActive, Enabled: true},
			{ID: "second", Provider: auth.SubscriptionProviderCodex, State: auth.SubscriptionAccountStateActive, Enabled: true},
		})
		body := `{"model":"auto","stream":true,"input":"synthetic","tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]}`
		rec := httptest.NewRecorder()
		require.NoError(t, svc.ProxyOpenAIResponses(ctx, []byte(body), rec, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))))
		require.Contains(t, rec.Body.String(), "answer")
	}
	mu.Lock()
	defer mu.Unlock()
	require.Positive(t, poolHits[0], "the first request walks the overloaded pool")
	require.Zero(t, poolHits[1], "the next request must not re-walk accounts that just answered overloaded")
}
