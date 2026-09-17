package subscriptions_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/subscriptions"
)

type runtimeStore struct {
	mu                   sync.Mutex
	accounts             []*auth.SubscriptionAccount
	refreshTokens        map[string][]byte
	accessTokens         map[string][]byte
	accessExpiry         map[string]time.Time
	tokenRefreshVersions map[string]int64
	leaseIDs             map[string]string
	leaseUntil           map[string]time.Time
	rotatedTokens        map[string][]byte
	enabledUpdates       map[string]bool
	cooldowns            map[string]time.Time
	stateErr             error
	extendErr            error
	extendCount          atomic.Int32
}

func (s *runtimeStore) ListSubscriptionAccounts(context.Context, string) ([]*auth.SubscriptionAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	accounts := make([]*auth.SubscriptionAccount, 0, len(s.accounts))
	for _, account := range s.accounts {
		copied := *account
		accounts = append(accounts, &copied)
	}
	return accounts, nil
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

func (s *runtimeStore) TryAcquireSubscriptionRefreshLease(_ context.Context, _ string, accountID, leaseID string, leaseTTL time.Duration) (auth.RefreshLeaseAcquisition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, account := range s.accounts {
		if account.ID == accountID && (!account.Enabled || (account.CooldownUntil != nil && account.CooldownUntil.After(now))) {
			return auth.RefreshLeaseAcquisition{}, nil
		}
	}
	if current := s.leaseUntil[accountID]; current.After(now) {
		return auth.RefreshLeaseAcquisition{}, nil
	}
	_, tookOver := s.leaseIDs[accountID]
	s.leaseIDs[accountID] = leaseID
	s.leaseUntil[accountID] = now.Add(leaseTTL)
	return auth.RefreshLeaseAcquisition{Acquired: true, TookOver: tookOver}, nil
}

func (s *runtimeStore) ExtendSubscriptionRefreshLease(_ context.Context, _ string, accountID, leaseID string, leaseTTL time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.extendCount.Add(1)
	if s.extendErr != nil {
		return false, s.extendErr
	}
	for _, account := range s.accounts {
		if account.ID == accountID && !account.Enabled {
			return false, nil
		}
	}
	if s.leaseIDs[accountID] != leaseID {
		return false, nil
	}
	s.leaseUntil[accountID] = time.Now().Add(leaseTTL)
	return true, nil
}

func (s *runtimeStore) ReleaseSubscriptionRefreshLease(ctx context.Context, _ string, accountID, leaseID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
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
		TokenRefreshVersion: s.tokenRefreshVersions[accountID],
		TokenRefreshLeaseID: s.leaseIDs[accountID],
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
	for _, account := range s.accounts {
		if account.ID == accountID && !account.Enabled {
			return auth.ErrSubscriptionRefreshConflict
		}
	}
	if s.leaseIDs[accountID] != leaseID || s.tokenRefreshVersions[accountID] != expectedVersion {
		return auth.ErrSubscriptionRefreshConflict
	}
	s.refreshTokens[accountID] = append([]byte(nil), refreshToken...)
	s.accessTokens[accountID] = append([]byte(nil), accessToken...)
	s.accessExpiry[accountID] = expiresAt
	s.tokenRefreshVersions[accountID]++
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
		tokenRefreshVersions: make(map[string]int64), leaseIDs: make(map[string]string), leaseUntil: make(map[string]time.Time),
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
	leaseResults := make(chan leaseResult, 2)
	go func() {
		lease, present, err := runtimeA.Lease(context.Background(), "owner-1", subscriptions.ProviderClaude, "session-a")
		leaseResults <- leaseResult{lease: lease, present: present, err: err}
	}()
	go func() {
		lease, present, err := runtimeB.Lease(context.Background(), "owner-1", subscriptions.ProviderClaude, "session-b")
		leaseResults <- leaseResult{lease: lease, present: present, err: err}
	}()
	<-refreshStarted
	require.Eventually(t, func() bool { return refreshes.Load() == 1 }, time.Second, time.Millisecond)
	close(allowRefresh)
	first, second := <-leaseResults, <-leaseResults
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
	acquisition, err := store.TryAcquireSubscriptionRefreshLease(context.Background(), "owner-1", "account-1", leaseID, time.Minute)
	require.NoError(t, err)
	require.True(t, acquisition.Acquired)
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
		store.tokenRefreshVersions["account-1"]++
		store.leaseIDs["account-1"] = "winner"
		store.leaseUntil["account-1"] = time.Now().Add(time.Minute)
		store.mu.Unlock()
		return subscriptions.RefreshedToken{}, &subscriptions.OAuthRefreshError{Provider: subscriptions.ProviderClaude, Status: 401}
	}), nil)

	lease, present, err := runtime.Lease(context.Background(), "owner-1", subscriptions.ProviderClaude, "session-1")
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, "access-winner", lease.AccessToken)
	require.Equal(t, "winner", store.leaseIDs["account-1"])
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
	require.Contains(t, store.enabledUpdates, "account-1")
	require.False(t, store.enabledUpdates["account-1"])
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
	require.Contains(t, store.enabledUpdates, "account-1")
	require.False(t, store.enabledUpdates["account-1"])
}

func (s *runtimeStore) DisableSubscriptionAccountIfRefreshHolder(_ context.Context, _ string, accountID, leaseID string, expectedVersion int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leaseIDs[accountID] != leaseID || s.tokenRefreshVersions[accountID] != expectedVersion {
		return auth.ErrSubscriptionRefreshConflict
	}
	s.enabledUpdates[accountID] = false
	if s.stateErr != nil {
		return s.stateErr
	}
	for _, account := range s.accounts {
		if account.ID == accountID {
			account.Enabled = false
			account.CooldownUntil = nil
		}
	}
	delete(s.leaseIDs, accountID)
	delete(s.leaseUntil, accountID)
	return nil
}

func (s *runtimeStore) CooldownSubscriptionAccountIfRefreshHolder(_ context.Context, _ string, accountID, leaseID string, expectedVersion int64, cooldownUntil time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leaseIDs[accountID] != leaseID || s.tokenRefreshVersions[accountID] != expectedVersion {
		return auth.ErrSubscriptionRefreshConflict
	}
	s.cooldowns[accountID] = cooldownUntil
	if s.stateErr != nil {
		return s.stateErr
	}
	for _, account := range s.accounts {
		if account.ID == accountID {
			account.CooldownUntil = &cooldownUntil
		}
	}
	delete(s.leaseIDs, accountID)
	delete(s.leaseUntil, accountID)
	return nil
}

type refreshFailureRaceStore struct {
	*runtimeStore
	beforeFailure func()
}

func (s *refreshFailureRaceStore) DisableSubscriptionAccountIfRefreshHolder(ctx context.Context, ownerID, accountID, leaseID string, expectedVersion int64) error {
	s.beforeFailure()
	return s.runtimeStore.DisableSubscriptionAccountIfRefreshHolder(ctx, ownerID, accountID, leaseID, expectedVersion)
}

func (s *refreshFailureRaceStore) CooldownSubscriptionAccountIfRefreshHolder(ctx context.Context, ownerID, accountID, leaseID string, expectedVersion int64, cooldownUntil time.Time) error {
	s.beforeFailure()
	return s.runtimeStore.CooldownSubscriptionAccountIfRefreshHolder(ctx, ownerID, accountID, leaseID, expectedVersion, cooldownUntil)
}

func TestRuntimeFencesFailureAfterCredentialReload(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			store := &refreshFailureRaceStore{runtimeStore: newRuntimeStore(&auth.SubscriptionAccount{
				ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderClaude, Enabled: true,
			})}
			store.refreshTokens["account-1"] = []byte("refresh-old")
			store.beforeFailure = func() {
				store.mu.Lock()
				store.leaseIDs["account-1"] = "winner"
				store.leaseUntil["account-1"] = time.Now().Add(time.Minute)
				store.mu.Unlock()
				require.NoError(t, store.PersistSubscriptionTokens(context.Background(), "owner-1", "account-1", "winner", 0,
					[]byte("refresh-new"), []byte("access-winner"), time.Now().Add(time.Hour)))
			}
			runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
				return subscriptions.RefreshedToken{}, &subscriptions.OAuthRefreshError{Provider: subscriptions.ProviderClaude, Status: status}
			}), nil)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			lease, present, err := runtime.Lease(ctx, "owner-1", subscriptions.ProviderClaude, "")
			require.NoError(t, err)
			require.True(t, present)
			require.Equal(t, "access-winner", lease.AccessToken)
			require.Empty(t, store.enabledUpdates)
			require.Empty(t, store.cooldowns)
			lease.Release()
		})
	}
}

func TestRuntimeReleasesCanceledRefreshLease(t *testing.T) {
	for _, requestErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(requestErr.Error(), func(t *testing.T) {
			store := newRuntimeStore(&auth.SubscriptionAccount{
				ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderClaude, Enabled: true,
			})
			store.refreshTokens["account-1"] = []byte("refresh-current")
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
				if errors.Is(requestErr, context.Canceled) {
					cancel()
				}
				<-ctx.Done()
				return subscriptions.RefreshedToken{}, ctx.Err()
			}), nil)
			_, present, err := runtime.Lease(ctx, "owner-1", subscriptions.ProviderClaude, "")
			require.ErrorIs(t, err, requestErr)
			require.True(t, present)
			require.Empty(t, store.leaseIDs)
			require.Empty(t, store.leaseUntil)
			require.Empty(t, store.enabledUpdates)
			require.Empty(t, store.cooldowns)
			acquisition, err := store.TryAcquireSubscriptionRefreshLease(context.Background(), "owner-1", "account-1", "next-holder", time.Minute)
			require.NoError(t, err)
			require.True(t, acquisition.Acquired)
		})
	}
}

func TestRuntimeReleasesOwnedLeaseWhenAdoptingNewCredentials(t *testing.T) {
	store := newRuntimeStore(&auth.SubscriptionAccount{
		ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderClaude, Enabled: true,
	})
	store.refreshTokens["account-1"] = []byte("refresh-old")
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.tokenRefreshVersions["account-1"]++
		store.refreshTokens["account-1"] = []byte("refresh-new")
		store.accessTokens["account-1"] = []byte("access-new")
		store.accessExpiry["account-1"] = time.Now().Add(time.Hour)
		return subscriptions.RefreshedToken{}, &subscriptions.OAuthRefreshError{Provider: subscriptions.ProviderClaude, Status: http.StatusUnauthorized}
	}), nil)
	lease, present, err := runtime.Lease(context.Background(), "owner-1", subscriptions.ProviderClaude, "")
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, "access-new", lease.AccessToken)
	require.Empty(t, store.leaseIDs)
	require.Empty(t, store.leaseUntil)
	require.Empty(t, store.enabledUpdates)
	lease.Release()
}

type refreshReleaseStore struct {
	*runtimeStore
	releasedLeaseIDs chan string
}

func (s *refreshReleaseStore) ReleaseSubscriptionRefreshLease(ctx context.Context, ownerID, accountID, leaseID string) error {
	if err := s.runtimeStore.ReleaseSubscriptionRefreshLease(ctx, ownerID, accountID, leaseID); err != nil {
		return err
	}
	s.releasedLeaseIDs <- leaseID
	return nil
}

func TestRuntimeStaleTerminalFailureBeforeWinnerPersists(t *testing.T) {
	store := &refreshReleaseStore{
		runtimeStore: newRuntimeStore(&auth.SubscriptionAccount{
			ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderClaude, Enabled: true,
		}),
		releasedLeaseIDs: make(chan string, 10),
	}
	store.refreshTokens["account-1"] = []byte("refresh-old")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	startedA, startedB := make(chan struct{}), make(chan struct{})
	finishA, finishB := make(chan struct{}), make(chan struct{})
	runtimeA := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(ctx context.Context, _ subscriptions.Provider, _ string) (subscriptions.RefreshedToken, error) {
		close(startedA)
		select {
		case <-finishA:
			return subscriptions.RefreshedToken{}, &subscriptions.OAuthRefreshError{Provider: subscriptions.ProviderClaude, Status: http.StatusUnauthorized}
		case <-ctx.Done():
			return subscriptions.RefreshedToken{}, ctx.Err()
		}
	}), nil)
	runtimeB := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(ctx context.Context, _ subscriptions.Provider, _ string) (subscriptions.RefreshedToken, error) {
		close(startedB)
		select {
		case <-finishB:
			return subscriptions.RefreshedToken{AccessToken: "access-winner", RefreshToken: "refresh-new", ExpiresAt: time.Now().Add(time.Hour)}, nil
		case <-ctx.Done():
			return subscriptions.RefreshedToken{}, ctx.Err()
		}
	}), nil)
	type leaseResult struct {
		lease subscriptions.Lease
		err   error
	}
	leaseResults := make(chan leaseResult, 2)
	go func() {
		lease, _, err := runtimeA.Lease(ctx, "owner-1", subscriptions.ProviderClaude, "session-a")
		leaseResults <- leaseResult{lease: lease, err: err}
	}()
	select {
	case <-startedA:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	store.mu.Lock()
	loserID := store.leaseIDs["account-1"]
	store.leaseUntil["account-1"] = time.Now().Add(-time.Second)
	store.mu.Unlock()
	go func() {
		lease, _, err := runtimeB.Lease(ctx, "owner-1", subscriptions.ProviderClaude, "session-b")
		leaseResults <- leaseResult{lease: lease, err: err}
	}()
	select {
	case <-startedB:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	close(finishA)
	select {
	case leaseID := <-store.releasedLeaseIDs:
		require.Equal(t, loserID, leaseID)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	store.mu.Lock()
	disabled := len(store.enabledUpdates) > 0
	winnerLeaseID := store.leaseIDs["account-1"]
	store.mu.Unlock()
	require.False(t, disabled)
	require.NotEmpty(t, winnerLeaseID)
	require.NotEqual(t, loserID, winnerLeaseID)
	close(finishB)
	for range 2 {
		select {
		case completedLease := <-leaseResults:
			require.NoError(t, completedLease.err)
			require.Equal(t, "access-winner", completedLease.lease.AccessToken)
			completedLease.lease.Release()
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	require.Equal(t, []byte("refresh-new"), store.refreshTokens["account-1"])
	require.True(t, store.accounts[0].Enabled)
	require.Empty(t, store.leaseIDs)
}

func TestRuntimeCurrentRefreshFailureSetsCooldownAndReleases(t *testing.T) {
	store := newRuntimeStore(&auth.SubscriptionAccount{
		ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderClaude, Enabled: true,
	})
	store.refreshTokens["account-1"] = []byte("refresh-current")
	now := time.Now()
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		return subscriptions.RefreshedToken{}, &subscriptions.OAuthRefreshError{Provider: subscriptions.ProviderClaude, Status: http.StatusServiceUnavailable}
	}), func() time.Time { return now })
	_, present, err := runtime.Lease(context.Background(), "owner-1", subscriptions.ProviderClaude, "")
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount)
	require.True(t, present)
	require.Equal(t, now.Add(time.Minute), store.cooldowns["account-1"])
	require.True(t, store.accounts[0].Enabled)
	require.Empty(t, store.leaseIDs)
}

// Amin's PR 1353 case: a provider call that outlives one lease window must not
// let a peer take over and double-spend the refresh token.
func TestRuntimeHeartbeatKeepsLeaseDuringSlowRefresh(t *testing.T) {
	store := newRuntimeStore(&auth.SubscriptionAccount{
		ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderClaude, Enabled: true,
	})
	store.refreshTokens["account-1"] = []byte("refresh-old")
	const leaseTTL = 60 * time.Millisecond
	peerBlocked := make(chan bool, 1)
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		// Outlive the original lease window by several heartbeats.
		time.Sleep(4 * leaseTTL)
		acquisition, err := store.TryAcquireSubscriptionRefreshLease(context.Background(), "owner-1", "account-1", "peer", leaseTTL)
		require.NoError(t, err)
		peerBlocked <- !acquisition.Acquired
		return subscriptions.RefreshedToken{AccessToken: "access-new", RefreshToken: "refresh-new", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}), nil)
	subscriptions.SetRefreshLeaseTimingForTest(runtime, leaseTTL, leaseTTL/3)

	lease, present, err := runtime.Lease(context.Background(), "owner-1", subscriptions.ProviderClaude, "")
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, "access-new", lease.AccessToken)
	require.True(t, <-peerBlocked, "peer acquired a lease that the heartbeat should have kept alive")
	require.GreaterOrEqual(t, store.extendCount.Load(), int32(1))
	require.Equal(t, []byte("refresh-new"), store.refreshTokens["account-1"])
	require.True(t, store.accounts[0].Enabled)
	require.Empty(t, store.enabledUpdates)
	lease.Release()
}

func TestRuntimeLostHeartbeatCancelsRefreshWithoutDisabling(t *testing.T) {
	store := newRuntimeStore(&auth.SubscriptionAccount{
		ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderClaude, Enabled: true,
	})
	store.refreshTokens["account-1"] = []byte("refresh-old")
	const leaseTTL = 60 * time.Millisecond
	var refreshes atomic.Int32
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(ctx context.Context, _ subscriptions.Provider, _ string) (subscriptions.RefreshedToken, error) {
		if refreshes.Add(1) == 1 {
			// Simulate an operator reset that nulls the lease while we are in flight.
			store.mu.Lock()
			delete(store.leaseIDs, "account-1")
			delete(store.leaseUntil, "account-1")
			store.mu.Unlock()
			<-ctx.Done()
			return subscriptions.RefreshedToken{}, ctx.Err()
		}
		return subscriptions.RefreshedToken{AccessToken: "access-new", RefreshToken: "refresh-new", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}), nil)
	subscriptions.SetRefreshLeaseTimingForTest(runtime, leaseTTL, leaseTTL/3)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	lease, present, err := runtime.Lease(ctx, "owner-1", subscriptions.ProviderClaude, "")
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, "access-new", lease.AccessToken, "lost lease should retry with a clean acquire")
	require.Equal(t, int32(2), refreshes.Load())
	require.Empty(t, store.enabledUpdates)
	require.Empty(t, store.cooldowns)
	lease.Release()
}

func TestRuntimeExtendErrorDoesNotCancelRefresh(t *testing.T) {
	store := newRuntimeStore(&auth.SubscriptionAccount{
		ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderClaude, Enabled: true,
	})
	store.refreshTokens["account-1"] = []byte("refresh-old")
	store.extendErr = errors.New("database blip")
	const leaseTTL = 60 * time.Millisecond
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(ctx context.Context, _ subscriptions.Provider, _ string) (subscriptions.RefreshedToken, error) {
		select {
		case <-time.After(3 * leaseTTL):
		case <-ctx.Done():
			return subscriptions.RefreshedToken{}, ctx.Err()
		}
		return subscriptions.RefreshedToken{AccessToken: "access-new", RefreshToken: "refresh-new", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}), nil)
	subscriptions.SetRefreshLeaseTimingForTest(runtime, leaseTTL, leaseTTL/3)

	lease, present, err := runtime.Lease(context.Background(), "owner-1", subscriptions.ProviderClaude, "")
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, "access-new", lease.AccessToken)
	require.GreaterOrEqual(t, store.extendCount.Load(), int32(1))
	lease.Release()
}

// A holder that took over an expired lease cannot tell a revoked token from one
// the previous holder already rotated, so it must fail closed instead of
// disabling the account.
func TestRuntimeTakeoverTerminalRefreshDoesNotDisable(t *testing.T) {
	store := newRuntimeStore(&auth.SubscriptionAccount{
		ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderClaude, Enabled: true,
	})
	store.refreshTokens["account-1"] = []byte("refresh-spent")
	store.leaseIDs["account-1"] = "crashed-holder"
	store.leaseUntil["account-1"] = time.Now().Add(-time.Second)
	var refreshes atomic.Int32
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		refreshes.Add(1)
		return subscriptions.RefreshedToken{}, &subscriptions.OAuthRefreshError{Provider: subscriptions.ProviderClaude, Status: http.StatusBadRequest}
	}), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, present, err := runtime.Lease(ctx, "owner-1", subscriptions.ProviderClaude, "")
	require.True(t, present)
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount)
	// First attempt took over and failed closed; the retry acquired cleanly and
	// disabled with certainty. In-memory pool state must not have disabled after
	// the first attempt, or the second would never have run.
	require.Equal(t, int32(2), refreshes.Load())
	require.Contains(t, store.enabledUpdates, "account-1")
	require.False(t, store.enabledUpdates["account-1"])
	require.Empty(t, store.cooldowns)
}

func TestRuntimeTakeoverTerminalRefreshAdoptsWinnerOnRetry(t *testing.T) {
	store := newRuntimeStore(&auth.SubscriptionAccount{
		ID: "account-1", APIKeyID: "owner-1", Provider: auth.SubscriptionProviderClaude, Enabled: true,
	})
	store.refreshTokens["account-1"] = []byte("refresh-spent")
	store.leaseIDs["account-1"] = "slow-holder"
	store.leaseUntil["account-1"] = time.Now().Add(-time.Second)
	var refreshes atomic.Int32
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		refreshes.Add(1)
		// The slow holder finishes its persist out of band after our takeover
		// (lease id/version fencing already rejects it in the real store; here we
		// model the state it leaves behind: a usable access token published).
		store.mu.Lock()
		store.accessTokens["account-1"] = []byte("access-winner")
		store.accessExpiry["account-1"] = time.Now().Add(time.Hour)
		store.mu.Unlock()
		return subscriptions.RefreshedToken{}, &subscriptions.OAuthRefreshError{Provider: subscriptions.ProviderClaude, Status: http.StatusBadRequest}
	}), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	lease, present, err := runtime.Lease(ctx, "owner-1", subscriptions.ProviderClaude, "")
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, "access-winner", lease.AccessToken)
	require.Equal(t, int32(1), refreshes.Load())
	require.Empty(t, store.enabledUpdates)
	require.Empty(t, store.cooldowns)
	lease.Release()
}
