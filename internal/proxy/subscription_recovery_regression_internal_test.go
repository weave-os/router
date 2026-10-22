package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

func TestSubscriptionRecoverySlowFailureUsesSecondSeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{
			{AccountID: "seat-a", AccessToken: "token-a"},
			{AccountID: "seat-b", AccessToken: "token-b"},
		}}
		provider := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{writeBytes: []byte("included answer")}}}
		svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: provider}).
			WithManagedSubscriptions(leaser).
			WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
		rec := httptest.NewRecorder()
		buf := newPreludeBuffer(rec)
		in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
		in.initialDecision = router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeOpus48.String()}
		var seats []string
		in.attempt = func(ctx context.Context, decision router.Decision, client providers.Client) error {
			creds := CredentialsFromContext(ctx)
			if creds != nil && creds.OAuth {
				seats = append(seats, creds.SubscriptionAccountID)
			}
			if len(seats) == 1 {
				time.Sleep(12 * time.Second)
				return &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}
			}
			buf.Seal()
			return client.Proxy(ctx, decision, providers.PreparedRequest{}, buf, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		}
		_, err := svc.dispatchWithFallback(managedSubscriptionTestContext(), in)
		require.NoError(t, err)
		require.Equal(t, []string{"seat-a", "seat-b"}, seats)
		require.Equal(t, "included answer", rec.Body.String())
	})
}

func TestSubscriptionRecoveryUnavailablePoolNeverBuysAPI(t *testing.T) {
	provider := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{writeBytes: []byte("paid answer")}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: provider}).
		WithManagedSubscriptions(&scriptedSubscriptionLeaser{}).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
	in.initialDecision = router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeOpus48.String()}
	_, err := svc.dispatchWithFallback(managedSubscriptionTestContext(), in)
	require.Error(t, err)
	require.Zero(t, provider.calls)
	require.NotContains(t, rec.Body.String(), "paid answer")
}
