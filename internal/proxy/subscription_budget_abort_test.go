package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

func TestSubscriptionBudgetAbortedCommitUsesLiveAPIFallback(t *testing.T) {
	client := &fakeClient{name: providers.ProviderOpenAI}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderOpenAI: client}).WithManagedSubscriptions(&scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "synthetic-seat", AccessToken: "seat-token"}}}).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
	ctx := managedSubscriptionContext(auth.SubscriptionProviderCodex)
	budget, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = context.WithValue(ctx, subscriptionRotationBudgetKey{}, budget)
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderOpenAI}}, nil)
	in.initialDecision = router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel}
	calls := 0
	in.attempt = func(attemptCtx context.Context, _ router.Decision, _ providers.Client) error {
		calls++
		buf.Seal()
		if calls == 1 {
			require.True(t, CredentialsFromContext(attemptCtx).OAuth)
			cancel()
			buf.abortIfUncommitted(buf.currentAttemptGeneration(), func() {})
			_, err := buf.Write([]byte("must not escape"))
			require.ErrorIs(t, err, errAttemptAborted)
			return err
		}
		require.NoError(t, attemptCtx.Err())
		creds := CredentialsFromContext(attemptCtx)
		require.True(t, creds == nil || !creds.OAuth)
		_, err := buf.Write([]byte("api rescue"))
		return err
	}
	_, err := svc.dispatchWithFallback(ctx, in)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, "api rescue", rec.Body.String())
}

func TestSubscriptionBudgetAbortedCommitRendersCleanErrorWithoutAPI(t *testing.T) {
	client := &fakeClient{name: providers.ProviderOpenAI}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderOpenAI: client}).WithManagedSubscriptions(&scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "synthetic-seat", AccessToken: "seat-token"}}})
	ctx := managedSubscriptionContext(auth.SubscriptionProviderCodex)
	budget, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = context.WithValue(ctx, subscriptionRotationBudgetKey{}, budget)
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderOpenAI}}, nil)
	in.initialDecision = router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel}
	in.flushErr = func(w http.ResponseWriter, err error) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("terminal error"))
	}
	calls := 0
	in.attempt = func(attemptCtx context.Context, _ router.Decision, _ providers.Client) error {
		calls++
		buf.Seal()
		if calls == 1 {
			require.True(t, CredentialsFromContext(attemptCtx).OAuth)
			cancel()
			buf.abortIfUncommitted(buf.currentAttemptGeneration(), func() {})
			_, err := buf.Write([]byte("must not escape"))
			require.ErrorIs(t, err, errAttemptAborted)
			return err
		}
		require.NoError(t, attemptCtx.Err())
		creds := CredentialsFromContext(attemptCtx)
		require.True(t, creds == nil || !creds.OAuth)
		_, err := buf.Write([]byte("api rescue"))
		return err
	}
	_, err := svc.dispatchWithFallback(ctx, in)
	require.Error(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, "terminal error", rec.Body.String())
}
