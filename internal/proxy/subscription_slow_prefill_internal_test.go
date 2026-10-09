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
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

func serveSlowSubscriptionTurn(t *testing.T, ctx context.Context, deploymentKeyed map[string]struct{}) (string, int32) {
	t.Helper()
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
		WithDeploymentKeyedProviders(deploymentKeyed)
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"synthetic slow prefill"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`
	rec := httptest.NewRecorder()
	err := svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	require.NoError(t, err)
	return rec.Body.String(), calls.Load()
}

func TestSubscriptionOnlySlowFirstOutputDoesNotUsePaidCapacity(t *testing.T) {
	ctx := billing.WithSubscriptionOnly(managedSubscriptionContext(auth.SubscriptionProviderCodex), billing.SubscriptionOnlyCreditsDepleted)
	body, calls := serveSlowSubscriptionTurn(t, ctx, map[string]struct{}{providers.ProviderOpenAI: {}})
	require.Contains(t, body, "slow subscription answer")
	require.EqualValues(t, 1, calls, "subscription-only requests must not use the available paid key")
}

// State-model policy forbids paid attempts only inside the active set; a
// funded request can still recover through its configured exhausted set.
func TestSubscriptionRotationTimeoutPreservesFundedRecovery(t *testing.T) {
	stateCtx := context.WithValue(context.Background(), InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateCodexModel})
	stateCtx = context.WithValue(stateCtx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{statePaidModel})
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want time.Duration
	}{
		{"funded configured sets", stateCtx, 10 * time.Second},
		{"linked first configured sets", billing.WithSubscriptionOnly(stateCtx, billing.SubscriptionOnlyLinkedFirst), 10 * time.Second},
		{"depleted configured sets", billing.WithSubscriptionOnly(stateCtx, billing.SubscriptionOnlyCreditsDepleted), 120 * time.Second},
		{"funded subscription attempt", context.WithValue(stateCtx, subscriptionOnlyAttemptKey{}, true), 10 * time.Second},
		{"ordinary funded", context.Background(), 10 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, (&Service{}).subscriptionRotationTimeout(tc.ctx)) })
	}
}

func TestSelfHostedSubscriptionSlowFirstOutputWithoutBilling(t *testing.T) {
	body, calls := serveSlowSubscriptionTurn(t, managedSubscriptionContext(auth.SubscriptionProviderCodex), map[string]struct{}{})
	require.Contains(t, body, "slow subscription answer")
	require.EqualValues(t, 1, calls, "self-hosted request must remain on the subscription")
}

func TestSubscriptionRotationTimeoutSelfHostedCredentials(t *testing.T) {
	svc := &Service{deploymentKeyedProviders: map[string]struct{}{}}
	ctx := context.Background()
	require.Equal(t, 120*time.Second, svc.subscriptionRotationTimeout(ctx))
	paidCtx := context.WithValue(ctx, CredentialsContextKey{}, &Credentials{APIKey: []byte("synthetic"), Source: credSourceClient})
	require.Equal(t, 10*time.Second, svc.subscriptionRotationTimeout(paidCtx))
	byokCtx := context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{{Provider: providers.ProviderOpenAI, Plaintext: []byte("synthetic")}})
	require.Equal(t, 10*time.Second, svc.subscriptionRotationTimeout(byokCtx))
	svc.deploymentKeyedProviders[providers.ProviderOpenAI] = struct{}{}
	require.Equal(t, 10*time.Second, svc.subscriptionRotationTimeout(ctx))
	svc.deploymentKeyedProviders = nil
	require.Equal(t, 10*time.Second, svc.subscriptionRotationTimeout(ctx))
}

func TestLinkedFirstWithoutDeploymentKeysRetainsFundedRecovery(t *testing.T) {
	svc := &Service{deploymentKeyedProviders: map[string]struct{}{}}
	ctx := billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyLinkedFirst)
	require.Equal(t, 10*time.Second, svc.subscriptionRotationTimeout(ctx))
	ctx = context.WithValue(ctx, CredentialsContextKey{}, codexSubscriptionCreds("synthetic-seat", "synthetic-account"))
	require.Equal(t, 10*time.Second, svc.subscriptionRotationTimeout(ctx))
}

func TestSubscriptionOnlyRotatesAfterSlowRetryableFailure(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{
		{AccountID: "opaque-a", AccessToken: "token-a"},
		{AccountID: "opaque-b", AccessToken: "token-b"},
	}}
	client := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{
		{err: &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests}},
		{writeBytes: []byte("served")},
	}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: client}).WithManagedSubscriptions(leaser)
	startedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// The first attempt takes 11s: every clock read before it sees the start,
	// every read after it is past the funded 10s cap but inside the 120s window.
	svc.now = func() time.Time {
		if client.calls == 0 {
			return startedAt
		}
		return startedAt.Add(11 * time.Second)
	}

	_, err := svc.dispatchWithFallback(billing.WithSubscriptionOnly(managedSubscriptionTestContext(), billing.SubscriptionOnlyCreditsDepleted), failoverInputs{
		w:               httptest.NewRecorder(),
		initialDecision: router.Decision{Model: "claude-opus-4-8", Provider: providers.ProviderAnthropic},
		purpose:         inference.PurposeAnthropicMessages,
		bindings:        []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}},
		attempt: func(ctx context.Context, decision router.Decision, client providers.Client) error {
			return client.Proxy(ctx, decision, providers.PreparedRequest{}, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		},
	})

	require.NoError(t, err)
	require.Equal(t, 2, client.calls)
	require.Equal(t, 2, leaser.next)
}
