package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

func TestCodexResetDiscardsDelayedPreResetObservation(t *testing.T) {
	observer := usage.NewObserver([]byte("reset-observation-test"), time.Minute, time.Now)
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "seat", AccessToken: "synthetic-token", ProviderAccount: "workspace"}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderOpenAI: &fakeClient{}}).
		WithManagedSubscriptions(leaser).WithUsageObserver(observer)
	ctx := svc.withUsageObserver(managedSubscriptionContext(auth.SubscriptionProviderCodex), http.Header{})
	started, resume, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		recorder := httptest.NewRecorder()
		_, err := svc.dispatchWithFallback(ctx, failoverInputs{
			w: recorder, buf: newPreludeBuffer(recorder), purpose: inference.PurposeOpenAIResponses,
			initialDecision: router.Decision{Model: stateCodexModel, Provider: providers.ProviderOpenAI},
			bindings:        []catalog.ProviderBinding{{Provider: providers.ProviderOpenAI}},
			attempt: func(ctx context.Context, _ router.Decision, _ providers.Client) error {
				close(started)
				<-resume
				providers.ObserveUpstreamHeaders(ctx, http.Header{
					"X-Codex-Primary-Used-Percent": {"100"}, "X-Codex-Primary-Window-Minutes": {"300"},
				})
				return nil
			},
		})
		done <- err
	}()
	<-started
	physical, token := observer.Key([]byte("subscription-account:seat")), observer.Key([]byte("synthetic-token"))
	observer.Forget(physical)
	observer.Forget(observer.Key([]byte("new-post-reset-token")))
	close(resume)
	require.NoError(t, <-done)
	_, found := observer.Snapshot(physical)
	require.False(t, found, "a callback from before verified recovery must not restore exhausted quota")
	_, found = observer.Snapshot(token)
	require.False(t, found, "the same stale callback must not repopulate the token alias")
}
