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

func expiredRotationContext(provider auth.SubscriptionProvider) context.Context {
	ctx := managedSubscriptionContext(provider)
	budget, cancel := context.WithCancel(ctx)
	cancel()
	return context.WithValue(ctx, subscriptionRotationBudgetKey{}, budget)
}

func TestExpiredRotationBudgetUsesLiveAPIFallbackWithoutSubscriptionAttempt(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "synthetic-seat", AccessToken: "seat-token"}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderOpenAI: &fakeClient{name: providers.ProviderOpenAI}}).WithManagedSubscriptions(leaser).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
	ctx := expiredRotationContext(auth.SubscriptionProviderCodex)
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderOpenAI}}, nil)
	in.initialDecision = router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel}
	var oauthCalls, apiCalls int
	in.attempt = func(attemptCtx context.Context, _ router.Decision, _ providers.Client) error {
		require.NoError(t, attemptCtx.Err(), "paid recovery runs on the caller's live context")
		if creds := CredentialsFromContext(attemptCtx); creds != nil && creds.OAuth {
			oauthCalls++
		} else {
			apiCalls++
		}
		buf.Seal()
		_, err := buf.Write([]byte("api rescue"))
		return err
	}

	_, err := svc.dispatchWithFallback(ctx, in)

	require.NoError(t, err)
	require.Zero(t, oauthCalls, "no included attempt is admitted after the rotation budget expired")
	require.Equal(t, 1, apiCalls)
	require.Equal(t, "api rescue", rec.Body.String())
	require.Empty(t, leaser.cooldownIDs, "budget expiry alone never cools down an account")
	require.False(t, ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage).Served)
}

func TestExpiredRotationBudgetRendersCleanErrorWithoutAPI(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "synthetic-seat", AccessToken: "seat-token"}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderOpenAI: &fakeClient{name: providers.ProviderOpenAI}}).WithManagedSubscriptions(leaser)
	ctx := expiredRotationContext(auth.SubscriptionProviderCodex)
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderOpenAI}}, nil)
	in.initialDecision = router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel}
	in.flushErr = func(w http.ResponseWriter, err error) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("terminal error"))
	}
	calls := 0
	in.attempt = func(context.Context, router.Decision, providers.Client) error {
		calls++
		return nil
	}

	_, err := svc.dispatchWithFallback(ctx, in)

	require.ErrorIs(t, err, ErrSubscriptionPoolExhausted)
	require.Zero(t, calls, "without an authorized paid key nothing is dispatched")
	require.Equal(t, "terminal error", rec.Body.String())
}
