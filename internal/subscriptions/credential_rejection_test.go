package subscriptions_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/subscriptions"
)

func (s *runtimeStore) RecordSubscriptionCredentialRejection(_ context.Context, _ auth.SubscriptionOwner, accountID string, input auth.SubscriptionCredentialRejectionInput) (auth.SubscriptionCredentialRejection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.LeaseID != "" && (s.leaseIDs[accountID] != input.LeaseID || s.tokenRefreshVersions[accountID] != input.ExpectedVersion) {
		return auth.SubscriptionCredentialRejection{}, auth.ErrSubscriptionRefreshConflict
	}
	reason, at, window := input.Reason, input.RejectedAt, input.Window
	if s.rejections == nil {
		s.rejections = make(map[string]auth.SubscriptionCredentialRejection)
		s.rejectionReasons = make(map[string]string)
	}
	current := s.rejections[accountID]
	previous := s.rejectionReasons[accountID]
	if current.Count == 0 || current.FirstRejectedAt.Before(at.Add(-window)) {
		current = auth.SubscriptionCredentialRejection{Count: 1, FirstRejectedAt: at}
	} else {
		current.Count++
	}
	s.rejections[accountID] = current
	s.rejectionReasons[accountID] = reason
	if input.InvalidateAccessToken {
		delete(s.accessTokens, accountID)
		delete(s.accessExpiry, accountID)
	}
	current.PreviousReason = previous
	return current, nil
}

type steppedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *steppedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *steppedClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newRejectionStore(provider auth.SubscriptionProvider) *runtimeStore {
	store := newRuntimeStore(&auth.SubscriptionAccount{
		ID: "account-1", SubscriberID: "subscriber-1", EnrolledByAPIKeyID: "key-1", Provider: provider,
		ExternalAccountID: "external-1", Enabled: true,
	})
	store.refreshTokens["account-1"] = []byte("refresh-secret")
	return store
}

// expireStoreCooldown stands in for wall-clock time passing in the fake's
// lease acquisition, which reads time.Now rather than the runtime clock.
func expireStoreCooldown(store *runtimeStore) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.accounts[0].CooldownUntil = nil
}

func TestRuntimeSingleTerminalRefreshRejectionCoolsDownInsteadOfDisabling(t *testing.T) {
	store := newRejectionStore(auth.SubscriptionProviderClaude)
	clock := &steppedClock{now: time.Now()}
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		return subscriptions.RefreshedToken{}, &subscriptions.OAuthRefreshError{Provider: subscriptions.ProviderClaude, Status: http.StatusBadRequest}
	}), clock.Now)

	_, present, err := runtime.Lease(context.Background(), testOwner, subscriptions.ProviderClaude, "")
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount)
	require.True(t, present)
	require.True(t, store.accounts[0].Enabled)
	require.NotContains(t, store.enabledUpdates, "account-1")
	require.Equal(t, clock.Now().Add(time.Minute), store.cooldowns["account-1"])
}

func TestRuntimeRepeatedInvalidGrantRequiresReconnect(t *testing.T) {
	store := newRejectionStore(auth.SubscriptionProviderClaude)
	clock := &steppedClock{now: time.Now()}
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		return subscriptions.RefreshedToken{}, &subscriptions.OAuthRefreshError{Provider: subscriptions.ProviderClaude, Status: http.StatusBadRequest, Code: "invalid_grant"}
	}), clock.Now)

	_, _, err := runtime.Lease(context.Background(), testOwner, subscriptions.ProviderClaude, "")
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount)
	require.True(t, store.accounts[0].Enabled)

	clock.Advance(2 * time.Minute)
	expireStoreCooldown(store)
	_, _, err = runtime.Lease(context.Background(), testOwner, subscriptions.ProviderClaude, "")
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount)
	require.False(t, store.accounts[0].Enabled)
	require.False(t, store.enabledUpdates["account-1"])
}

func TestRuntimeRejectedCredentialNeedsSustainedStrikesBeforeReconnect(t *testing.T) {
	store := newRejectionStore(auth.SubscriptionProviderCodex)
	store.accessTokens["account-1"] = []byte("access-live")
	clock := &steppedClock{now: time.Now()}
	runtime := subscriptions.NewRuntime(store, nil, clock.Now)

	for strike := 1; strike <= 3; strike++ {
		reconnect, err := runtime.RejectCredential(context.Background(), testOwner, subscriptions.ProviderCodex, "account-1", http.StatusUnauthorized)
		require.NoError(t, err)
		require.False(t, reconnect, "strike %d within a short burst must not require reconnect", strike)
		clock.Advance(time.Hour)
	}
	require.Empty(t, store.accessTokens["account-1"], "a rejected access token must be dropped so the next lease refreshes")
	require.NotEqual(t, auth.SubscriptionAccountStateReconnectRequired, store.healthStates["account-1"])

	clock.Advance(4 * time.Hour)
	reconnect, err := runtime.RejectCredential(context.Background(), testOwner, subscriptions.ProviderCodex, "account-1", http.StatusUnauthorized)
	require.NoError(t, err)
	require.True(t, reconnect)
	require.Equal(t, auth.SubscriptionAccountStateReconnectRequired, store.healthStates["account-1"])
	require.False(t, store.enabledUpdates["account-1"])
}

func TestRuntimeRejectionStrikesExpireAfterWindow(t *testing.T) {
	store := newRejectionStore(auth.SubscriptionProviderCodex)
	clock := &steppedClock{now: time.Now()}
	runtime := subscriptions.NewRuntime(store, nil, clock.Now)

	for range 2 {
		_, err := runtime.RejectCredential(context.Background(), testOwner, subscriptions.ProviderCodex, "account-1", http.StatusUnauthorized)
		require.NoError(t, err)
		clock.Advance(13 * time.Hour)
	}
	reconnect, err := runtime.RejectCredential(context.Background(), testOwner, subscriptions.ProviderCodex, "account-1", http.StatusUnauthorized)
	require.NoError(t, err)
	require.False(t, reconnect)
}

func TestRuntimeCodexRefreshWithoutUserClaimKeepsAccount(t *testing.T) {
	store := newRuntimeStore(&auth.SubscriptionAccount{
		ID: "account-1", SubscriberID: "subscriber-1", EnrolledByAPIKeyID: "key-1", Provider: auth.SubscriptionProviderCodex,
		ExternalAccountID: "workspace", ProviderUserID: "provider-user-a", Enabled: true,
	})
	store.refreshTokens["account-1"] = []byte("refresh-old")
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		return subscriptions.RefreshedToken{AccessToken: "access", RefreshToken: "refresh-new", AccountID: "workspace", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}), nil)

	lease, _, err := runtime.Lease(context.Background(), testOwner, subscriptions.ProviderCodex, "")
	require.NoError(t, err)
	require.Equal(t, "access", lease.AccessToken)
	require.True(t, store.accounts[0].Enabled)
	lease.Release()
}

// seedSustainedRejections leaves the account one strike short of reconnect,
// with strikes spanning longer than the minimum span.
func seedSustainedRejections(store *runtimeStore, accountID string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.rejections = map[string]auth.SubscriptionCredentialRejection{accountID: {Count: 2, FirstRejectedAt: time.Now().Add(-7 * time.Hour)}}
	store.rejectionReasons = map[string]string{accountID: "refresh_http_401"}
}

func TestRuntimeStaleRefresherRecordsNoRejectionStrike(t *testing.T) {
	store := newRejectionStore(auth.SubscriptionProviderClaude)
	clock := &steppedClock{now: time.Now()}
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		store.mu.Lock()
		store.leaseIDs["account-1"] = "replacement-lease"
		store.mu.Unlock()
		return subscriptions.RefreshedToken{}, &subscriptions.OAuthRefreshError{Provider: subscriptions.ProviderClaude, Status: http.StatusBadRequest, Code: "invalid_grant"}
	}), clock.Now)

	_, _, err := runtime.Lease(context.Background(), testOwner, subscriptions.ProviderClaude, "")
	require.Error(t, err)
	require.Zero(t, store.rejections["account-1"].Count, "a refresher that lost its lease must not strike replacement credentials")
	require.NotContains(t, store.cooldowns, "account-1")
	require.True(t, store.accounts[0].Enabled)
}

func TestRuntimeSuccessfulRefreshBreaksInvalidGrantChain(t *testing.T) {
	store := newRejectionStore(auth.SubscriptionProviderClaude)
	clock := &steppedClock{now: time.Now()}
	var calls int
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		calls++
		if calls == 2 {
			return subscriptions.RefreshedToken{AccessToken: "access", RefreshToken: "refresh-new", ExpiresAt: time.Now().Add(time.Hour)}, nil
		}
		return subscriptions.RefreshedToken{}, &subscriptions.OAuthRefreshError{Provider: subscriptions.ProviderClaude, Status: http.StatusBadRequest, Code: "invalid_grant"}
	}), clock.Now)

	_, _, err := runtime.Lease(context.Background(), testOwner, subscriptions.ProviderClaude, "")
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount)
	clock.Advance(2 * time.Minute)
	expireStoreCooldown(store)
	lease, _, err := runtime.Lease(context.Background(), testOwner, subscriptions.ProviderClaude, "")
	require.NoError(t, err)
	lease.Release()

	store.mu.Lock()
	delete(store.accessTokens, "account-1")
	store.mu.Unlock()
	_, _, err = runtime.Lease(context.Background(), testOwner, subscriptions.ProviderClaude, "")
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount)
	require.Equal(t, 3, calls)
	require.True(t, store.accounts[0].Enabled, "invalid_grant separated by a successful refresh is not back-to-back")
	require.Equal(t, 2, store.rejections["account-1"].Count)
}

func TestRuntimeReplicaDropsCachedTokenRejectedElsewhere(t *testing.T) {
	store := newRejectionStore(auth.SubscriptionProviderCodex)
	var calls int
	var mu sync.Mutex
	refresh := runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return subscriptions.RefreshedToken{
			AccessToken: fmt.Sprintf("access-%d", calls), RefreshToken: fmt.Sprintf("refresh-%d", calls),
			AccountID: "external-1", ExpiresAt: time.Now().Add(time.Hour),
		}, nil
	})
	rejecting := subscriptions.NewRuntime(store, refresh, nil)
	other := subscriptions.NewRuntime(store, refresh, nil)

	first, _, err := rejecting.Lease(context.Background(), testOwner, subscriptions.ProviderCodex, "")
	require.NoError(t, err)
	first.Release()
	cached, _, err := other.Lease(context.Background(), testOwner, subscriptions.ProviderCodex, "")
	require.NoError(t, err)
	rejectedToken := cached.AccessToken
	cached.Release()

	_, err = rejecting.RejectCredential(context.Background(), testOwner, subscriptions.ProviderCodex, "account-1", http.StatusUnauthorized)
	require.NoError(t, err)
	expireStoreCooldown(store)

	next, _, err := other.Lease(context.Background(), testOwner, subscriptions.ProviderCodex, "")
	require.NoError(t, err)
	defer next.Release()
	require.NotEqual(t, rejectedToken, next.AccessToken, "a replica must not replay a token another replica saw rejected")
}

func TestRuntimeFailedDisconnectWriteDoesNotEmitCommittedDisconnect(t *testing.T) {
	store := newRejectionStore(auth.SubscriptionProviderClaude)
	seedSustainedRejections(store, "account-1")
	store.stateErr = fmt.Errorf("synthetic disconnect write rejected")
	var output bytes.Buffer
	ctx := observability.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&output, nil)))
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		return subscriptions.RefreshedToken{}, &subscriptions.OAuthRefreshError{Provider: subscriptions.ProviderClaude, Status: http.StatusUnauthorized}
	}), nil)
	_, _, err := runtime.Lease(ctx, testOwner, subscriptions.ProviderClaude, "")
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount)
	require.NotContains(t, output.String(), `"subscription_event":"reconnect_required"`, "a rejected state write must not page as a committed disconnect")
	require.Contains(t, output.String(), "synthetic disconnect write rejected")
}

type simultaneousCredentialRejections struct {
	*runtimeStore
	recorded sync.WaitGroup
}

func (s *simultaneousCredentialRejections) RecordSubscriptionCredentialRejection(ctx context.Context, owner auth.SubscriptionOwner, accountID string, input auth.SubscriptionCredentialRejectionInput) (auth.SubscriptionCredentialRejection, error) {
	rejection, err := s.runtimeStore.RecordSubscriptionCredentialRejection(ctx, owner, accountID, input)
	s.recorded.Done()
	s.recorded.Wait()
	return rejection, err
}

func TestRuntimeConcurrentRejectionsEmitOneDisconnectSignal(t *testing.T) {
	store := &simultaneousCredentialRejections{runtimeStore: newRejectionStore(auth.SubscriptionProviderClaude)}
	seedSustainedRejections(store.runtimeStore, "account-1")
	store.recorded.Add(2)
	var output bytes.Buffer
	ctx := observability.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&output, nil)))
	runtime := subscriptions.NewRuntime(store, nil, nil)
	var callers sync.WaitGroup
	callers.Add(2)
	failures := make(chan error, 2)
	for range 2 {
		go func() {
			defer callers.Done()
			_, err := runtime.RejectCredential(ctx, testOwner, subscriptions.ProviderClaude, "account-1", http.StatusUnauthorized)
			failures <- err
		}()
	}
	callers.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	require.Equal(t, 1, strings.Count(output.String(), `"subscription_event":"reconnect_required"`))
}
