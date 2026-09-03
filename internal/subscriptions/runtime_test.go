package subscriptions_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"workweave/router/internal/auth"
	"workweave/router/internal/subscriptions"
)

type runtimeStore struct {
	mu             sync.Mutex
	accounts       []*auth.SubscriptionAccount
	refreshTokens  map[string][]byte
	rotatedTokens  map[string][]byte
	enabledUpdates map[string]bool
	cooldowns      map[string]time.Time
	stateErr       error
}

func (s *runtimeStore) ListSubscriptionAccounts(context.Context, string) ([]*auth.SubscriptionAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*auth.SubscriptionAccount(nil), s.accounts...), nil
}

func (s *runtimeStore) SubscriptionRefreshToken(_ context.Context, _ string, accountID string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.refreshTokens[accountID]...), nil
}

func (s *runtimeStore) UpdateSubscriptionRefreshToken(_ context.Context, _ string, accountID string, token []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rotatedTokens[accountID] = append([]byte(nil), token...)
	return nil
}

func (s *runtimeStore) UpdateSubscriptionAccountState(_ context.Context, _ string, accountID string, enabled bool, _ *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabledUpdates[accountID] = enabled
	return s.stateErr
}

func (s *runtimeStore) UpdateSubscriptionAccountCooldown(_ context.Context, _ string, accountID string, cooldownUntil time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cooldowns[accountID] = cooldownUntil
	return s.stateErr
}

type runtimeRefreshFunc func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error)

func (f runtimeRefreshFunc) Refresh(ctx context.Context, provider subscriptions.Provider, token string) (subscriptions.RefreshedToken, error) {
	return f(ctx, provider, token)
}

func newRuntimeStore(accounts ...*auth.SubscriptionAccount) *runtimeStore {
	return &runtimeStore{
		accounts: accounts, refreshTokens: make(map[string][]byte),
		rotatedTokens: make(map[string][]byte), enabledUpdates: make(map[string]bool), cooldowns: make(map[string]time.Time),
	}
}

func TestRuntimeCooldownDoesNotWriteEnabledState(t *testing.T) {
	store := newRuntimeStore(&auth.SubscriptionAccount{
		ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderClaude, Enabled: true,
	})
	store.refreshTokens["account-1"] = []byte("refresh-secret")
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		return subscriptions.RefreshedToken{AccessToken: "access", RefreshToken: "refresh-secret", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}), nil)
	lease, present, err := runtime.Lease(context.Background(), "owner-1", subscriptions.ProviderClaude, "")
	require.NoError(t, err)
	require.True(t, present)
	lease.Release()

	resetAt := time.Now().Add(time.Minute)
	require.NoError(t, runtime.Cooldown(context.Background(), "owner-1", subscriptions.ProviderClaude, "account-1", resetAt))
	require.Equal(t, resetAt, store.cooldowns["account-1"])
	require.Empty(t, store.enabledUpdates)
}

func TestRuntimeCoalescesRefreshAndPersistsRotation(t *testing.T) {
	store := newRuntimeStore(&auth.SubscriptionAccount{
		ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderCodex,
		ExternalAccountID: "chatgpt-1", Enabled: true,
	})
	store.refreshTokens["account-1"] = []byte("refresh-old")
	var refreshes atomic.Int32
	refresher := runtimeRefreshFunc(func(_ context.Context, provider subscriptions.Provider, token string) (subscriptions.RefreshedToken, error) {
		refreshes.Add(1)
		require.Equal(t, subscriptions.ProviderCodex, provider)
		require.Equal(t, "refresh-old", token)
		time.Sleep(10 * time.Millisecond)
		return subscriptions.RefreshedToken{
			AccessToken: "access", RefreshToken: "refresh-new", AccountID: "chatgpt-1",
			ExpiresAt: time.Now().Add(time.Hour),
		}, nil
	})
	runtime := subscriptions.NewRuntime(store, refresher, nil)

	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			lease, present, err := runtime.Lease(context.Background(), "owner-1", subscriptions.ProviderCodex, "session-1")
			require.NoError(t, err)
			require.True(t, present)
			require.Equal(t, "access", lease.AccessToken)
			require.Equal(t, "chatgpt-1", lease.ProviderAccount)
			lease.Release()
		}()
	}
	wait.Wait()

	require.Equal(t, int32(1), refreshes.Load())
	require.Equal(t, []byte("refresh-new"), store.rotatedTokens["account-1"])
}

func TestRuntimeDisablesTerminallyRejectedAccount(t *testing.T) {
	store := newRuntimeStore(&auth.SubscriptionAccount{
		ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderClaude,
		ExternalAccountID: "claude-1", Enabled: true,
	})
	store.refreshTokens["account-1"] = []byte("rejected")
	refresher := runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		return subscriptions.RefreshedToken{}, &subscriptions.OAuthRefreshError{Provider: subscriptions.ProviderClaude, Status: 401}
	})
	runtime := subscriptions.NewRuntime(store, refresher, nil)

	_, present, err := runtime.Lease(context.Background(), "owner-1", subscriptions.ProviderClaude, "")
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount)
	require.True(t, present)
	require.Equal(t, false, store.enabledUpdates["account-1"])
}

func TestRuntimeDoesNotCooldownCanceledRefresh(t *testing.T) {
	store := newRuntimeStore(&auth.SubscriptionAccount{
		ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderClaude, Enabled: true,
	})
	store.refreshTokens["account-1"] = []byte("refresh-secret")
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		return subscriptions.RefreshedToken{}, context.Canceled
	}), nil)

	_, present, err := runtime.Lease(context.Background(), "owner-1", subscriptions.ProviderClaude, "")
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, present)
	require.Empty(t, store.enabledUpdates)
}

func TestRuntimePreservesTerminalClassificationWhenStatePersistenceFails(t *testing.T) {
	store := newRuntimeStore(&auth.SubscriptionAccount{
		ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderClaude, Enabled: true,
	})
	store.refreshTokens["account-1"] = []byte("refresh-secret")
	store.stateErr = errors.New("database unavailable")
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		return subscriptions.RefreshedToken{}, &subscriptions.OAuthRefreshError{Provider: subscriptions.ProviderClaude, Status: 401}
	}), nil)

	_, present, err := runtime.Lease(context.Background(), "owner-1", subscriptions.ProviderClaude, "")
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount)
	require.True(t, present)
	require.Equal(t, false, store.enabledUpdates["account-1"])
}
