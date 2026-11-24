package subscriptions_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/subscriptions"
)

type quotaProberFunc func(context.Context, subscriptions.Provider, string, string) (subscriptions.QuotaProbe, error)

func (f quotaProberFunc) ProbeQuota(ctx context.Context, provider subscriptions.Provider, accessToken, accountID string) (subscriptions.QuotaProbe, error) {
	return f(ctx, provider, accessToken, accountID)
}

func exhaustSeatUntil(f *proactiveRefreshFixture, reset time.Time) {
	f.store.accounts[0].State = auth.SubscriptionAccountStateExhausted
	f.store.accounts[0].CooldownUntil = &reset
}

func TestManualRefreshReactivatesSeatWhenProviderQuotaWasReset(t *testing.T) {
	f := newProactiveRefreshFixture(t, false, false)
	f.accessAt(t, 0)
	exhaustSeatUntil(f, f.started.Add(3*simulatedDay))
	var probedWith, probedAccount string
	f.runtime.WithQuotaProber(quotaProberFunc(func(_ context.Context, provider subscriptions.Provider, accessToken, accountID string) (subscriptions.QuotaProbe, error) {
		require.Equal(t, subscriptions.ProviderCodex, provider)
		probedWith, probedAccount = accessToken, accountID
		return subscriptions.QuotaProbe{Available: true}, nil
	}))
	result, err := f.runtime.RefreshAccount(context.Background(), testOwner, "synthetic-seat")
	require.NoError(t, err)
	require.True(t, result.QuotaReactivated)
	require.Equal(t, string(f.store.accessTokens["synthetic-seat"]), probedWith, "probe with the freshly refreshed access token")
	require.Equal(t, "synthetic-workspace", probedAccount)
	require.Equal(t, auth.SubscriptionAccountStateActive, f.store.accounts[0].State)
	require.Nil(t, f.store.accounts[0].CooldownUntil)
	lease, present, err := f.runtime.Lease(context.Background(), testOwner, subscriptions.ProviderCodex, "synthetic-session")
	require.NoError(t, err)
	require.True(t, present)
	lease.Release()
}

func TestManualRefreshKeepsSeatExhaustedWhenQuotaCannotBeConfirmed(t *testing.T) {
	for name, probe := range map[string]quotaProberFunc{
		"still_exhausted": func(context.Context, subscriptions.Provider, string, string) (subscriptions.QuotaProbe, error) {
			return subscriptions.QuotaProbe{ResetAt: time.Now().Add(time.Hour)}, nil
		},
		"probe_failed": func(context.Context, subscriptions.Provider, string, string) (subscriptions.QuotaProbe, error) {
			return subscriptions.QuotaProbe{}, errors.New("synthetic usage endpoint outage")
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newProactiveRefreshFixture(t, false, false)
			f.accessAt(t, 0)
			reset := f.started.Add(3 * simulatedDay)
			exhaustSeatUntil(f, reset)
			f.runtime.WithQuotaProber(probe)
			result, err := f.runtime.RefreshAccount(context.Background(), testOwner, "synthetic-seat")
			require.NoError(t, err)
			require.False(t, result.QuotaReactivated)
			require.Equal(t, auth.SubscriptionAccountStateExhausted, f.store.accounts[0].State)
			require.Equal(t, reset, *f.store.accounts[0].CooldownUntil)
			_, _, err = f.runtime.Lease(context.Background(), testOwner, subscriptions.ProviderCodex, "synthetic-session")
			require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount)
		})
	}
}

func TestManualRefreshSkipsQuotaProbeForRoutableSeat(t *testing.T) {
	f := newProactiveRefreshFixture(t, false, false)
	f.accessAt(t, 0)
	f.runtime.WithQuotaProber(quotaProberFunc(func(context.Context, subscriptions.Provider, string, string) (subscriptions.QuotaProbe, error) {
		t.Fatal("a routable seat needs no quota re-check")
		return subscriptions.QuotaProbe{}, nil
	}))
	result, err := f.runtime.RefreshAccount(context.Background(), testOwner, "synthetic-seat")
	require.NoError(t, err)
	require.False(t, result.QuotaReactivated)
}

func TestQuotaReactivationReachesReplicaHoldingStaleExhaustion(t *testing.T) {
	f := newProactiveRefreshFixture(t, false, false)
	f.accessAt(t, 0)
	clock := func() time.Time { return time.Unix(0, f.now.Load()).UTC() }
	replica := subscriptions.NewRuntime(f.store, f.issuer, clock)
	lease, _, err := replica.Lease(context.Background(), testOwner, subscriptions.ProviderCodex, "synthetic-session")
	require.NoError(t, err)
	lease.Release()
	reset := f.started.Add(3 * simulatedDay)
	require.NoError(t, replica.Exhaust(context.Background(), testOwner, subscriptions.ProviderCodex, "synthetic-seat", reset))

	f.runtime.WithQuotaProber(quotaProberFunc(func(context.Context, subscriptions.Provider, string, string) (subscriptions.QuotaProbe, error) {
		return subscriptions.QuotaProbe{Available: true}, nil
	}))
	result, err := f.runtime.RefreshAccount(context.Background(), testOwner, "synthetic-seat")
	require.NoError(t, err)
	require.True(t, result.QuotaReactivated)

	lease, present, err := replica.Lease(context.Background(), testOwner, subscriptions.ProviderCodex, "synthetic-session")
	require.NoError(t, err, "the newer credential generation carries the reactivation to other replicas")
	require.True(t, present)
	lease.Release()
}
