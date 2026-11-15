package subscriptions_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/subscriptions"
)

func TestManualRefreshRotatesValidCredentialsWithoutResettingQuota(t *testing.T) {
	f := newProactiveRefreshFixture(t, false, false)
	f.accessAt(t, 0)
	quotaReset := f.started.Add(time.Hour)
	f.store.accounts[0].State = auth.SubscriptionAccountStateExhausted
	f.store.accounts[0].CooldownUntil = &quotaReset
	result, err := f.runtime.RefreshAccount(context.Background(), testOwner, "synthetic-seat")
	require.NoError(t, err)
	require.Equal(t, int64(2), result.TokenRefreshVersion)
	require.Equal(t, 2, f.issuer.refreshes)
	require.Equal(t, auth.SubscriptionAccountStateExhausted, f.store.accounts[0].State)
	require.Equal(t, quotaReset, *f.store.accounts[0].CooldownUntil)
	require.Equal(t, []byte("synthetic-refresh-2"), f.store.refreshTokens["synthetic-seat"])
}

func TestManualRefreshCannotRotateSharedOrDisabledSeats(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "shared"}[foreign], func(t *testing.T) {
			f := newProactiveRefreshFixture(t, false, false)
			owner := testOwner
			if foreign {
				owner = auth.SubscriptionOwner{SubscriberID: "another-subscriber", APIKeyID: "another-key"}
			} else {
				f.store.accounts[0].Enabled = false
			}
			_, err := f.runtime.RefreshAccount(context.Background(), owner, "synthetic-seat")
			require.Error(t, err)
			if foreign {
				require.ErrorIs(t, err, auth.ErrSubscriptionAccountNotFound)
			} else {
				require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount)
			}
			require.Zero(t, f.issuer.refreshes)
			require.Zero(t, f.store.persistCalls)
		})
	}
}

func TestManualTransientRefreshFailureDoesNotCooldownValidSeat(t *testing.T) {
	f := newProactiveRefreshFixture(t, false, false)
	f.accessAt(t, 0)
	failure := errors.New("synthetic temporary issuer outage")
	runtime := subscriptions.NewRuntime(f.store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		return subscriptions.RefreshedToken{}, failure
	}), func() time.Time { return f.started })
	_, err := runtime.RefreshAccount(context.Background(), testOwner, "synthetic-seat")
	require.ErrorIs(t, err, failure)
	require.Empty(t, f.store.cooldowns)
	require.Empty(t, f.store.enabledUpdates)
	require.Equal(t, int64(1), f.store.tokenRefreshVersions["synthetic-seat"])
	require.True(t, f.issuer.accepts(string(f.store.accessTokens["synthetic-seat"])))
}

func TestEarlyRefreshTransientFailureKeepsValidAccess(t *testing.T) {
	f := newProactiveRefreshFixture(t, false, false)
	access := f.accessAt(t, 0)
	var attempts int
	clock := func() time.Time { return time.Unix(0, f.now.Load()).UTC() }
	policy, err := subscriptions.NewEarlyRefreshPolicy([]string{"synthetic-seat"}, 6*simulatedDay)
	require.NoError(t, err)
	f.runtime = subscriptions.NewRuntime(f.store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		attempts++
		return subscriptions.RefreshedToken{}, errors.New("synthetic temporary issuer outage")
	}), clock).WithEarlyRefreshPolicy(policy)
	require.Equal(t, access, f.accessAt(t, 6*simulatedDay))
	require.Equal(t, access, f.accessAt(t, 6*simulatedDay+2*time.Minute))
	require.Equal(t, 1, attempts, "back off the early refresh while the actual access token remains usable")
	require.Empty(t, f.store.cooldowns)
	require.Equal(t, int64(1), f.store.tokenRefreshVersions["synthetic-seat"])
}

type observedRefreshStore struct {
	*runtimeStore
	loads atomic.Int32
}

func (s *observedRefreshStore) LoadSubscriptionCredentials(ctx context.Context, owner auth.SubscriptionOwner, id string) (auth.SubscriptionCredentials, error) {
	credentials, err := s.runtimeStore.LoadSubscriptionCredentials(ctx, owner, id)
	s.loads.Add(1)
	return credentials, err
}

func TestConcurrentManualRefreshAdoptsThePublishedGeneration(t *testing.T) {
	f := newProactiveRefreshFixture(t, false, false)
	f.accessAt(t, 0)
	store := &observedRefreshStore{runtimeStore: f.store}
	started, allow := make(chan struct{}), make(chan struct{})
	refresher := runtimeRefreshFunc(func(ctx context.Context, provider subscriptions.Provider, token string) (subscriptions.RefreshedToken, error) {
		close(started)
		select {
		case <-allow:
			return f.issuer.Refresh(ctx, provider, token)
		case <-ctx.Done():
			return subscriptions.RefreshedToken{}, ctx.Err()
		}
	})
	type outcome struct {
		result subscriptions.AccountRefreshResult
		err    error
	}
	results := make(chan outcome, 2)
	refresh := func() {
		runtime := subscriptions.NewRuntime(store, refresher, func() time.Time { return f.started })
		result, err := runtime.RefreshAccount(context.Background(), testOwner, "synthetic-seat")
		results <- outcome{result: result, err: err}
	}
	go refresh()
	<-started
	go refresh()
	require.Eventually(t, func() bool { return store.loads.Load() >= 4 }, time.Second, time.Millisecond)
	close(allow)
	for range 2 {
		completed := <-results
		require.NoError(t, completed.err)
		require.Equal(t, int64(2), completed.result.TokenRefreshVersion)
	}
	require.Equal(t, 2, f.issuer.refreshes, "the concurrent manual calls spend one additional refresh token")
}

func TestManualRefreshDoesNotMakeQuotaExhaustedSeatRoutable(t *testing.T) {
	f := newProactiveRefreshFixture(t, false, false)
	f.accessAt(t, 0)
	reset := f.started.Add(time.Hour)
	f.store.accounts[0].State = auth.SubscriptionAccountStateExhausted
	f.store.accounts[0].CooldownUntil = &reset
	_, err := f.runtime.RefreshAccount(context.Background(), testOwner, "synthetic-seat")
	require.NoError(t, err)
	_, _, err = f.runtime.Lease(context.Background(), testOwner, subscriptions.ProviderCodex, "synthetic-session")
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount)
	require.Equal(t, 2, f.issuer.refreshes)
	require.Equal(t, reset, *f.store.accounts[0].CooldownUntil)
}

func TestRejectedManualRefreshPreservesQuotaReset(t *testing.T) {
	f := newProactiveRefreshFixture(t, false, false)
	f.accessAt(t, 0)
	reset := f.started.Add(3 * simulatedDay)
	f.store.accounts[0].State = auth.SubscriptionAccountStateExhausted
	f.store.accounts[0].CooldownUntil = &reset
	runtime := subscriptions.NewRuntime(f.store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		return subscriptions.RefreshedToken{}, &subscriptions.OAuthRefreshError{
			Provider: subscriptions.ProviderCodex, Status: http.StatusUnauthorized, Code: "refresh_token_invalidated"}
	}), func() time.Time { return f.started })
	_, err := runtime.RefreshAccount(context.Background(), testOwner, "synthetic-seat")
	require.Error(t, err)
	require.Equal(t, auth.SubscriptionAccountStateExhausted, f.store.accounts[0].State)
	require.Equal(t, reset, *f.store.accounts[0].CooldownUntil)
	require.Empty(t, f.store.cooldowns)
	require.Empty(t, f.store.enabledUpdates)
	require.Equal(t, int64(1), f.store.tokenRefreshVersions["synthetic-seat"])
}
