package subscriptions_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/subscriptions"
)

type runtimeStore struct {
	mu             sync.Mutex
	accounts       []*auth.SubscriptionAccount
	refreshTokens  map[string][]byte
	accessTokens   map[string][]byte
	accessExpiry   map[string]time.Time
	versions       map[string]int64
	leaseIDs       map[string]string
	leaseUntil     map[string]time.Time
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

func (s *runtimeStore) TryAcquireSubscriptionRefreshLease(_ context.Context, _ string, accountID, leaseID string, now, leaseUntil time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.leaseUntil[accountID]; current.After(now) {
		return false, nil
	}
	s.leaseIDs[accountID] = leaseID
	s.leaseUntil[accountID] = leaseUntil
	return true, nil
}

func (s *runtimeStore) ReleaseSubscriptionRefreshLease(_ context.Context, _ string, accountID, leaseID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leaseIDs[accountID] == leaseID {
		delete(s.leaseIDs, accountID)
		delete(s.leaseUntil, accountID)
	}
	return nil
}

func (s *runtimeStore) LoadSubscriptionCredentials(_ context.Context, _ string, accountID string) (auth.SubscriptionCredentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	enabled := true
	var cooldownUntil *time.Time
	for _, account := range s.accounts {
		if account.ID == accountID {
			enabled = account.Enabled
			cooldownUntil = account.CooldownUntil
			break
		}
	}
	credentials := auth.SubscriptionCredentials{
		RefreshToken:        append([]byte(nil), s.refreshTokens[accountID]...),
		AccessToken:         append([]byte(nil), s.accessTokens[accountID]...),
		TokenRefreshVersion: s.versions[accountID],
		Enabled:             enabled,
		CooldownUntil:       cooldownUntil,
	}
	if expiry := s.accessExpiry[accountID]; !expiry.IsZero() {
		value := expiry
		credentials.AccessTokenExpiresAt = &value
	}
	return credentials, nil
}

func (s *runtimeStore) PersistSubscriptionTokens(_ context.Context, _ string, accountID, leaseID string, expectedVersion int64, refreshToken, accessToken []byte, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leaseIDs[accountID] != leaseID || s.versions[accountID] != expectedVersion {
		return auth.ErrSubscriptionRefreshConflict
	}
	s.refreshTokens[accountID] = append([]byte(nil), refreshToken...)
	s.accessTokens[accountID] = append([]byte(nil), accessToken...)
	s.accessExpiry[accountID] = expiresAt
	s.versions[accountID]++
	delete(s.leaseIDs, accountID)
	delete(s.leaseUntil, accountID)
	s.rotatedTokens[accountID] = append([]byte(nil), refreshToken...)
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
		accessTokens: make(map[string][]byte), accessExpiry: make(map[string]time.Time),
		versions: make(map[string]int64), leaseIDs: make(map[string]string), leaseUntil: make(map[string]time.Time),
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

func TestRuntimeCoordinatesRefreshAcrossRuntimes(t *testing.T) {
	store := newRuntimeStore(&auth.SubscriptionAccount{
		ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderClaude, Enabled: true,
	})
	store.refreshTokens["account-1"] = []byte("refresh-old")
	refreshStarted := make(chan struct{})
	allowRefresh := make(chan struct{})
	var refreshes atomic.Int32
	refresher := runtimeRefreshFunc(func(ctx context.Context, provider subscriptions.Provider, token string) (subscriptions.RefreshedToken, error) {
		refreshes.Add(1)
		require.Equal(t, subscriptions.ProviderClaude, provider)
		require.Equal(t, "refresh-old", token)
		select {
		case refreshStarted <- struct{}{}:
		case <-ctx.Done():
			return subscriptions.RefreshedToken{}, ctx.Err()
		}
		select {
		case <-allowRefresh:
			return subscriptions.RefreshedToken{AccessToken: "access-shared", RefreshToken: "refresh-new", ExpiresAt: time.Now().Add(time.Hour)}, nil
		case <-ctx.Done():
			return subscriptions.RefreshedToken{}, ctx.Err()
		}
	})
	runtimeA := subscriptions.NewRuntime(store, refresher, nil)
	runtimeB := subscriptions.NewRuntime(store, refresher, nil)
	type leaseResult struct {
		lease   subscriptions.Lease
		present bool
		err     error
	}
	results := make(chan leaseResult, 2)
	go func() {
		lease, present, err := runtimeA.Lease(context.Background(), "owner-1", subscriptions.ProviderClaude, "session-a")
		results <- leaseResult{lease: lease, present: present, err: err}
	}()
	go func() {
		lease, present, err := runtimeB.Lease(context.Background(), "owner-1", subscriptions.ProviderClaude, "session-b")
		results <- leaseResult{lease: lease, present: present, err: err}
	}()
	<-refreshStarted
	require.Eventually(t, func() bool { return refreshes.Load() == 1 }, time.Second, time.Millisecond)
	close(allowRefresh)
	first, second := <-results, <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.True(t, first.present)
	require.True(t, second.present)
	require.Equal(t, "access-shared", first.lease.AccessToken)
	require.Equal(t, "access-shared", second.lease.AccessToken)
	first.lease.Release()
	second.lease.Release()
	require.Equal(t, int32(1), refreshes.Load())
	require.Equal(t, []byte("refresh-new"), store.refreshTokens["account-1"])
}

func TestRuntimeAdoptsAccessTokenWhileAnotherRuntimeRefreshes(t *testing.T) {
	store := newRuntimeStore(&auth.SubscriptionAccount{
		ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderClaude, Enabled: true,
	})
	store.refreshTokens["account-1"] = []byte("refresh-current")
	store.accessTokens["account-1"] = []byte("access-winner")
	store.accessExpiry["account-1"] = time.Now().Add(time.Hour)
	leaseID := "00000000-0000-0000-0000-000000000001"
	acquired, err := store.TryAcquireSubscriptionRefreshLease(context.Background(), "owner-1", "account-1", leaseID, time.Now(), time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, acquired)
	var refreshes atomic.Int32
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		refreshes.Add(1)
		return subscriptions.RefreshedToken{}, errors.New("refresh should not be called")
	}), nil)

	lease, present, err := runtime.Lease(context.Background(), "owner-1", subscriptions.ProviderClaude, "session-1")
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, "access-winner", lease.AccessToken)
	require.Equal(t, int32(0), refreshes.Load())
	lease.Release()
}

func TestRuntimeTakesOverExpiredRefreshLease(t *testing.T) {
	store := newRuntimeStore(&auth.SubscriptionAccount{
		ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderClaude, Enabled: true,
	})
	store.refreshTokens["account-1"] = []byte("refresh-old")
	store.leaseIDs["account-1"] = "expired-holder"
	store.leaseUntil["account-1"] = time.Now().Add(-time.Second)
	var refreshes atomic.Int32
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		refreshes.Add(1)
		return subscriptions.RefreshedToken{AccessToken: "access-recovered", RefreshToken: "refresh-new", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}), nil)

	lease, present, err := runtime.Lease(context.Background(), "owner-1", subscriptions.ProviderClaude, "session-1")
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, "access-recovered", lease.AccessToken)
	require.Equal(t, int32(1), refreshes.Load())
	lease.Release()
}

func TestRuntimeDoesNotDisableAfterStaleTerminalRefresh(t *testing.T) {
	store := newRuntimeStore(&auth.SubscriptionAccount{
		ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderClaude, Enabled: true,
	})
	store.refreshTokens["account-1"] = []byte("refresh-old")
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		store.mu.Lock()
		store.refreshTokens["account-1"] = []byte("refresh-new")
		store.accessTokens["account-1"] = []byte("access-winner")
		store.accessExpiry["account-1"] = time.Now().Add(time.Hour)
		store.versions["account-1"]++
		store.leaseIDs["account-1"] = "winner"
		store.leaseUntil["account-1"] = time.Now().Add(time.Minute)
		store.mu.Unlock()
		return subscriptions.RefreshedToken{}, &subscriptions.OAuthRefreshError{Provider: subscriptions.ProviderClaude, Status: 401}
	}), nil)

	lease, present, err := runtime.Lease(context.Background(), "owner-1", subscriptions.ProviderClaude, "session-1")
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, "access-winner", lease.AccessToken)
	require.Empty(t, store.enabledUpdates)
	lease.Release()
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
