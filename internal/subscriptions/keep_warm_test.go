package subscriptions_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/subscriptions"
)

func (s *runtimeStore) ListSubscriptionAccountsDueForKeepWarm(_ context.Context, refreshedBefore time.Time, limit int32) ([]*auth.SubscriptionAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []*auth.SubscriptionAccount
	for _, account := range s.accounts {
		if !account.Enabled || account.State == auth.SubscriptionAccountStateReconnectRequired || account.State == auth.SubscriptionAccountStateDisabled {
			continue
		}
		if at, ok := s.refreshedAt[account.ID]; ok && !at.Before(refreshedBefore) {
			continue
		}
		copied := *account
		due = append(due, &copied)
		if len(due) == int(limit) {
			break
		}
	}
	return due, nil
}

var keepWarmWeek = subscriptions.KeepWarmPolicy{MaxAge: 7 * 24 * time.Hour, BatchLimit: 10}

func keepWarmSeat(id string, provider auth.SubscriptionProvider, enabled bool, state auth.SubscriptionAccountState) *auth.SubscriptionAccount {
	return &auth.SubscriptionAccount{ID: id, SubscriberID: "subscriber-1", EnrolledByAPIKeyID: "key-1", Provider: provider,
		ExternalAccountID: "external-" + id, Enabled: enabled, State: state}
}

func newKeepWarmStore(clock func() time.Time, seats ...*auth.SubscriptionAccount) *runtimeStore {
	store := newRuntimeStore(seats...)
	store.clock = clock
	for _, seat := range seats {
		store.refreshTokens[seat.ID] = []byte("refresh-original-" + seat.ID)
	}
	return store
}

func rotatingRefresher(clock func() time.Time, calls *atomic.Int32) runtimeRefreshFunc {
	return func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		n := calls.Add(1)
		return subscriptions.RefreshedToken{AccessToken: fmt.Sprintf("access-rotated-%d", n), RefreshToken: fmt.Sprintf("refresh-rotated-%d", n),
			ExpiresAt: clock().Add(8 * time.Hour)}, nil
	}
}

func TestRuntimeKeepWarmRefreshesOnlyIdleEligibleSeats(t *testing.T) {
	clock := &steppedClock{now: time.Now()}
	store := newKeepWarmStore(clock.Now,
		keepWarmSeat("claude-idle", auth.SubscriptionProviderClaude, true, auth.SubscriptionAccountStateActive),
		keepWarmSeat("codex-exhausted-idle", auth.SubscriptionProviderCodex, true, auth.SubscriptionAccountStateExhausted),
		keepWarmSeat("claude-recent", auth.SubscriptionProviderClaude, true, auth.SubscriptionAccountStateActive),
		keepWarmSeat("claude-disabled", auth.SubscriptionProviderClaude, false, auth.SubscriptionAccountStateDisabled),
		keepWarmSeat("codex-reconnect", auth.SubscriptionProviderCodex, false, auth.SubscriptionAccountStateReconnectRequired),
	)
	store.refreshedAt["claude-idle"] = clock.Now().Add(-8 * 24 * time.Hour)
	store.refreshedAt["claude-recent"] = clock.Now().Add(-24 * time.Hour)
	var calls atomic.Int32
	runtime := subscriptions.NewRuntime(store, rotatingRefresher(clock.Now, &calls), clock.Now)

	result, err := runtime.KeepWarm(context.Background(), keepWarmWeek)
	require.NoError(t, err)
	require.Equal(t, subscriptions.KeepWarmResult{Due: 2, Refreshed: 2}, result)
	require.Equal(t, int32(2), calls.Load())
	for _, id := range []string{"claude-idle", "codex-exhausted-idle"} {
		credentials, err := store.LoadSubscriptionCredentials(context.Background(), testOwner, id)
		require.NoError(t, err)
		require.Contains(t, string(credentials.RefreshToken), "refresh-rotated-", "%s: the rotated refresh token is persisted", id)
		require.Contains(t, string(credentials.AccessToken), "access-rotated-", "%s: the rotated access token is persisted", id)
		require.Equal(t, int64(1), credentials.TokenRefreshVersion)
	}
	for _, id := range []string{"claude-recent", "claude-disabled", "codex-reconnect"} {
		require.Equal(t, "refresh-original-"+id, string(store.refreshTokens[id]), "%s must be untouched", id)
		require.Zero(t, store.tokenRefreshVersions[id], "%s must be untouched", id)
	}

	result, err = runtime.KeepWarm(context.Background(), keepWarmWeek)
	require.NoError(t, err)
	require.Zero(t, result.Due, "seats refreshed by the previous tick are not due again")
}

func TestRuntimeKeepWarmBoundsEachTick(t *testing.T) {
	clock := &steppedClock{now: time.Now()}
	store := newKeepWarmStore(clock.Now,
		keepWarmSeat("seat-a", auth.SubscriptionProviderClaude, true, auth.SubscriptionAccountStateActive),
		keepWarmSeat("seat-b", auth.SubscriptionProviderClaude, true, auth.SubscriptionAccountStateActive),
		keepWarmSeat("seat-c", auth.SubscriptionProviderCodex, true, auth.SubscriptionAccountStateActive),
	)
	var calls atomic.Int32
	runtime := subscriptions.NewRuntime(store, rotatingRefresher(clock.Now, &calls), clock.Now)

	result, err := runtime.KeepWarm(context.Background(), subscriptions.KeepWarmPolicy{MaxAge: keepWarmWeek.MaxAge, BatchLimit: 2})
	require.NoError(t, err)
	require.Equal(t, 2, result.Refreshed)
	require.Equal(t, int32(2), calls.Load())
}

func TestRuntimeKeepWarmConcurrentRefreshersExchangeOnce(t *testing.T) {
	for _, tc := range []struct {
		name    string
		compete func(*subscriptions.Runtime) error
	}{
		{name: "another replica's sweep", compete: func(runtime *subscriptions.Runtime) error {
			_, err := runtime.KeepWarm(context.Background(), keepWarmWeek)
			return err
		}},
		{name: "a request-path refresh", compete: func(runtime *subscriptions.Runtime) error {
			lease, _, err := runtime.Lease(context.Background(), testOwner, subscriptions.ProviderClaude, "")
			if err == nil {
				lease.Release()
			}
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newKeepWarmStore(time.Now, keepWarmSeat("claude-idle", auth.SubscriptionProviderClaude, true, auth.SubscriptionAccountStateActive))
			var calls atomic.Int32
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			refresher := runtimeRefreshFunc(func(ctx context.Context, provider subscriptions.Provider, token string) (subscriptions.RefreshedToken, error) {
				once.Do(func() { close(entered) })
				<-release
				return rotatingRefresher(time.Now, &calls)(ctx, provider, token)
			})
			sweeper := subscriptions.NewRuntime(store, refresher, time.Now)
			competitor := subscriptions.NewRuntime(store, refresher, time.Now)

			sweepDone := make(chan error, 1)
			go func() {
				_, err := sweeper.KeepWarm(context.Background(), keepWarmWeek)
				sweepDone <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("the sweep never reached the token exchange")
			}
			competeDone := make(chan error, 1)
			go func() { competeDone <- tc.compete(competitor) }()
			time.Sleep(50 * time.Millisecond)
			close(release)

			require.NoError(t, <-sweepDone)
			require.NoError(t, <-competeDone)
			require.Equal(t, int32(1), calls.Load(), "the refresh-holder lease must allow exactly one exchange")
			require.Equal(t, int64(1), store.tokenRefreshVersions["claude-idle"])
		})
	}
}

func TestRuntimeKeepWarmFailureNeverDisconnectsSeat(t *testing.T) {
	clock := &steppedClock{now: time.Now()}
	store := newKeepWarmStore(clock.Now, keepWarmSeat("claude-idle", auth.SubscriptionProviderClaude, true, auth.SubscriptionAccountStateActive))
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(_ context.Context, provider subscriptions.Provider, _ string) (subscriptions.RefreshedToken, error) {
		return subscriptions.RefreshedToken{}, &subscriptions.OAuthRefreshError{Provider: provider, Status: http.StatusBadRequest, Code: "invalid_grant"}
	}), clock.Now)

	for range 3 {
		result, err := runtime.KeepWarm(context.Background(), keepWarmWeek)
		require.NoError(t, err)
		require.Equal(t, subscriptions.KeepWarmResult{Due: 1, Failed: 1}, result)
		clock.Advance(time.Hour)
	}
	require.True(t, store.accounts[0].Enabled, "a background refresh failure must not require reconnect")
	require.NotEqual(t, auth.SubscriptionAccountStateReconnectRequired, store.healthStates["claude-idle"])
	require.Zero(t, store.rejections["claude-idle"].Count)
}

func TestRuntimeKeepWarmExchangeSurvivesSweepCancellation(t *testing.T) {
	store := newKeepWarmStore(time.Now, keepWarmSeat("claude-idle", auth.SubscriptionProviderClaude, true, auth.SubscriptionAccountStateActive))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(refreshCtx context.Context, provider subscriptions.Provider, token string) (subscriptions.RefreshedToken, error) {
		cancel()
		if err := refreshCtx.Err(); err != nil {
			return subscriptions.RefreshedToken{}, err
		}
		return rotatingRefresher(time.Now, &calls)(refreshCtx, provider, token)
	}), time.Now)

	_, _ = runtime.KeepWarm(ctx, keepWarmWeek)
	require.Eventually(t, func() bool {
		credentials, err := store.LoadSubscriptionCredentials(context.Background(), testOwner, "claude-idle")
		return err == nil && string(credentials.RefreshToken) == "refresh-rotated-1"
	}, 5*time.Second, 10*time.Millisecond, "a token the provider already rotated must be persisted")
}

type refreshedElsewhereStore struct {
	*runtimeStore
}

func (s refreshedElsewhereStore) ListSubscriptionAccountsDueForKeepWarm(ctx context.Context, refreshedBefore time.Time, limit int32) ([]*auth.SubscriptionAccount, error) {
	due, err := s.runtimeStore.ListSubscriptionAccountsDueForKeepWarm(ctx, refreshedBefore, limit)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, seat := range due {
		s.refreshedAt[seat.ID] = s.clock()
		s.tokenRefreshVersions[seat.ID]++
	}
	return due, err
}

func TestRuntimeKeepWarmSkipsSeatRefreshedAfterListing(t *testing.T) {
	store := newKeepWarmStore(time.Now, keepWarmSeat("claude-idle", auth.SubscriptionProviderClaude, true, auth.SubscriptionAccountStateActive))
	var calls atomic.Int32
	runtime := subscriptions.NewRuntime(refreshedElsewhereStore{store}, rotatingRefresher(time.Now, &calls), time.Now)

	result, err := runtime.KeepWarm(context.Background(), keepWarmWeek)
	require.NoError(t, err)
	require.Equal(t, subscriptions.KeepWarmResult{Due: 1, Skipped: 1}, result)
	require.Zero(t, calls.Load(), "a seat another refresher already renewed must not be exchanged again")
}
