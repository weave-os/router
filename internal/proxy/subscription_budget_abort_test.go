package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"

	"github.com/stretchr/testify/require"
)

func expiredRotationContext(provider auth.SubscriptionProvider) context.Context {
	ctx := managedSubscriptionContext(provider)
	return context.WithValue(ctx, credentials.SubscriptionRotationBudgetKey{}, &credentials.SubscriptionAdmissionBudget{})
}

func TestExpiredAdmissionBudgetDoesNotAuthorizeAPI(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "synthetic-seat", AccessToken: "seat-token"}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderOpenAI: &fakeClient{name: providers.ProviderOpenAI}}).WithManagedSubscriptions(leaser).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
	ctx := expiredRotationContext(auth.SubscriptionProviderCodex)
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(t.Context(), rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderOpenAI}}, nil)
	in.InitialDecision = router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel}
	var oauthCalls, apiCalls int
	in.Attempt = func(attemptCtx context.Context, _ router.Decision, _ dispatch.Upstream) error {
		require.NoError(t, attemptCtx.Err(), "paid recovery runs on the caller's live context")
		if creds := credentials.CredentialsFromContext(attemptCtx); creds != nil && creds.OAuth {
			oauthCalls++
		} else {
			apiCalls++
		}
		buf.Seal()
		_, err := buf.Write([]byte("api rescue"))
		return err
	}

	_, err := fallbackFor(svc).Dispatch(ctx, in)

	require.ErrorIs(t, err, credentials.ErrSubscriptionPoolUnavailable)
	require.Zero(t, oauthCalls, "no included attempt is admitted after the rotation budget expired")
	require.Zero(t, apiCalls)
	require.Empty(t, rec.Body.String())
	require.Empty(t, leaser.cooldownIDs, "budget expiry alone never cools down an account")
	require.False(t, mustAs[*credentials.ManagedSubscriptionUsage](t, ctx.Value(credentials.ManagedSubscriptionUsageContextKey{})).Served)
}

func TestExpiredRotationBudgetRendersCleanErrorWithoutAPI(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "synthetic-seat", AccessToken: "seat-token"}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderOpenAI: &fakeClient{name: providers.ProviderOpenAI}}).WithManagedSubscriptions(leaser)
	ctx := expiredRotationContext(auth.SubscriptionProviderCodex)
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(t.Context(), rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderOpenAI}}, nil)
	in.InitialDecision = router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel}
	in.FlushErr = func(w http.ResponseWriter, err error) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("terminal error"))
	}
	calls := 0
	in.Attempt = func(context.Context, router.Decision, dispatch.Upstream) error {
		calls++
		return nil
	}

	_, err := fallbackFor(svc).Dispatch(ctx, in)

	require.ErrorIs(t, err, credentials.ErrSubscriptionPoolUnavailable)
	class, classified := ClassifyDispatchError(err)
	require.True(t, classified)
	require.Equal(t, http.StatusServiceUnavailable, class.Status, "a spent admission budget is not proof the pool is exhausted")
	require.Zero(t, calls, "without an authorized paid key nothing is dispatched")
	require.Equal(t, "terminal error", rec.Body.String())
}
