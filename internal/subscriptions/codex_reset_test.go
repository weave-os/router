package subscriptions_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/subscriptions"
)

type resetRedemption struct{ account, credit, request string }

type resetProvider struct {
	mu                         sync.Mutex
	credits                    map[string][]subscriptions.ResetCredit
	exhausted                  map[string]bool
	quotaErr                   error
	consumeErr                 error
	stayExhausted              bool
	redemptions                []resetRedemption
	quotaReads, inventoryReads int
	started, resume            chan struct{}
}

func (p *resetProvider) CodexQuotaExhausted(_ context.Context, lease subscriptions.Lease) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.quotaReads++
	return p.exhausted[lease.AccountID], p.quotaErr
}
func (p *resetProvider) CodexResetCredits(_ context.Context, lease subscriptions.Lease) ([]subscriptions.ResetCredit, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inventoryReads++
	return p.credits[lease.AccountID], nil
}
func (p *resetProvider) ConsumeCodexReset(ctx context.Context, lease subscriptions.Lease, credit, request string) (subscriptions.ResetOutcome, error) {
	if p.started != nil {
		close(p.started)
		select {
		case <-p.resume:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.redemptions = append(p.redemptions, resetRedemption{lease.AccountID, credit, request})
	if p.consumeErr != nil {
		return "", p.consumeErr
	}
	if !p.stayExhausted {
		p.exhausted[lease.AccountID] = false
	}
	return subscriptions.ResetApplied, nil
}

type memoryResetStore struct {
	mu       sync.Mutex
	claim    subscriptions.ResetClaim
	active   bool
	acquires int
	accounts *runtimeStore
}

func (s *memoryResetStore) AcquireCodexReset(_ context.Context, subscriber, lease string, _ time.Duration) (subscriptions.ResetClaim, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquires++
	if s.active {
		return subscriptions.ResetClaim{}, false, nil
	}
	s.active = true
	s.claim.SubscriberID, s.claim.LeaseID = subscriber, lease
	return s.claim, true, nil
}
func (s *memoryResetStore) SelectCodexReset(_ context.Context, claim subscriptions.ResetClaim) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || claim.LeaseID != s.claim.LeaseID {
		return subscriptions.ErrResetClaimLost
	}
	s.claim = claim
	return nil
}
func (s *memoryResetStore) RecoverCodexAccount(_ context.Context, claim subscriptions.ResetClaim, accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || claim.LeaseID != s.claim.LeaseID {
		return subscriptions.ErrResetClaimLost
	}
	s.accounts.mu.Lock()
	defer s.accounts.mu.Unlock()
	for _, account := range s.accounts.accounts {
		if account.ID == accountID && account.Enabled {
			account.State, account.CooldownUntil = auth.SubscriptionAccountStateActive, nil
			if s.claim.AccountID == accountID {
				s.claim.AccountID, s.claim.CreditID, s.claim.RequestID = "", "", ""
			}
			return nil
		}
	}
	return subscriptions.ErrResetClaimLost
}
func (s *memoryResetStore) ClearCodexReset(_ context.Context, claim subscriptions.ResetClaim) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || claim.LeaseID != s.claim.LeaseID {
		return subscriptions.ErrResetClaimLost
	}
	s.claim.AccountID, s.claim.CreditID, s.claim.RequestID = "", "", ""
	return nil
}
func (s *memoryResetStore) ReleaseCodexReset(_ context.Context, claim subscriptions.ResetClaim) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if claim.LeaseID != s.claim.LeaseID {
		return subscriptions.ErrResetClaimLost
	}
	s.active = false
	return nil
}

func resetFixture(t *testing.T) (*subscriptions.Runtime, *runtimeStore, *resetProvider, *memoryResetStore, context.Context) {
	t.Helper()
	resetAt := time.Now().Add(24 * time.Hour)
	accounts := newRuntimeStore(
		&auth.SubscriptionAccount{ID: "a", SubscriberID: testOwner.SubscriberID, Provider: auth.SubscriptionProviderCodex, ExternalAccountID: "workspace-a", Enabled: true, State: auth.SubscriptionAccountStateExhausted, CooldownUntil: &resetAt},
		&auth.SubscriptionAccount{ID: "b", SubscriberID: testOwner.SubscriberID, Provider: auth.SubscriptionProviderCodex, ExternalAccountID: "workspace-b", Enabled: true, State: auth.SubscriptionAccountStateExhausted, CooldownUntil: &resetAt},
	)
	for _, account := range accounts.accounts {
		accounts.accessTokens[account.ID] = []byte("token-" + account.ID)
		accounts.accessExpiry[account.ID] = time.Now().Add(time.Hour)
	}
	provider := &resetProvider{exhausted: map[string]bool{"a": true, "b": true}, credits: map[string][]subscriptions.ResetCredit{
		"a": {{ID: "later", ExpiresAt: resetAt}}, "b": {{ID: "earliest", ExpiresAt: time.Now().Add(time.Hour)}},
	}}
	store := &memoryResetStore{accounts: accounts}
	runtime := subscriptions.NewRuntime(accounts, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		return subscriptions.RefreshedToken{}, errors.New("unexpected token refresh")
	}), nil).WithCodexResets(provider, store)
	ctx := flags.WithOverrides(context.Background(), flags.Overrides{Bools: map[flags.Key]bool{flags.KeyCodexAutoUsageReset: true}})
	return runtime, accounts, provider, store, ctx
}

func TestCodexResetDefaultsOff(t *testing.T) {
	runtime, _, provider, store, _ := resetFixture(t)
	_, recovered, err := runtime.ResetCodex(context.Background(), testOwner, "session")
	require.NoError(t, err)
	require.False(t, recovered)
	require.Zero(t, store.acquires)
	require.Zero(t, provider.quotaReads)
	require.Empty(t, provider.redemptions)
}

func TestCodexResetPersonalRollout(t *testing.T) {
	for _, tc := range []struct {
		name, subscriber, allowlist string
		enabled, want               bool
	}{
		{name: "selected subscriber", subscriber: testOwner.SubscriberID, allowlist: testOwner.SubscriberID, enabled: true, want: true},
		{name: "other subscriber", subscriber: "other-person", allowlist: testOwner.SubscriberID, enabled: true},
		{name: "no verified subscriber", allowlist: testOwner.SubscriberID, enabled: true},
		{name: "default remains off", subscriber: testOwner.SubscriberID, allowlist: testOwner.SubscriberID},
		{name: "comma separated", subscriber: testOwner.SubscriberID, allowlist: "other-person, " + testOwner.SubscriberID + " ", enabled: true, want: true},
		{name: "blank entry is not wildcard", subscriber: testOwner.SubscriberID, allowlist: " , ", enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime, _, provider, store, _ := resetFixture(t)
			ctx := flags.WithOverrides(context.Background(), flags.Overrides{
				Bools:   map[flags.Key]bool{flags.KeyCodexAutoUsageReset: tc.enabled},
				Strings: map[flags.Key]string{flags.KeyCodexAutoUsageResetSubscribers: tc.allowlist},
			})
			owner := testOwner
			owner.SubscriberID = tc.subscriber
			_, recovered, err := runtime.ResetCodex(ctx, owner, "session")
			require.NoError(t, err)
			require.Equal(t, tc.want, recovered)
			if !tc.want {
				// Rejected users cannot probe accounts or acquire a reset claim.
				require.Zero(t, store.acquires)
				require.Zero(t, provider.quotaReads)
				require.Empty(t, provider.redemptions)
			}
		})
	}
}

func TestCodexResetSelectsEarliestAndKeepsSessionOnRecoveredAccount(t *testing.T) {
	runtime, accounts, provider, store, ctx := resetFixture(t)
	// Establish an in-memory cooldown as well as persisted exhaustion.
	accounts.accounts[1].State, accounts.accounts[1].CooldownUntil = auth.SubscriptionAccountStateActive, nil
	primed, present, err := runtime.Lease(ctx, testOwner, subscriptions.ProviderCodex, "session")
	require.NoError(t, err)
	require.True(t, present)
	primed.Release()
	accounts.accessTokens["b"] = []byte("new-token-b")
	require.NoError(t, runtime.Exhaust(ctx, testOwner, subscriptions.ProviderCodex, "b", time.Now().Add(time.Hour)))
	lease, recovered, err := runtime.ResetCodex(ctx, testOwner, "session")
	require.NoError(t, err)
	require.True(t, recovered)
	require.Equal(t, "b", lease.AccountID)
	require.Equal(t, "workspace-b", lease.ProviderAccount)
	require.Equal(t, "new-token-b", lease.AccessToken)
	require.Equal(t, "earliest", provider.redemptions[0].credit)
	require.Empty(t, store.claim.CreditID)
	require.Nil(t, accounts.accounts[1].CooldownUntil)
	// Both accounts now have headroom; affinity keeps using the reset account.
	accounts.accounts[0].State, accounts.accounts[0].CooldownUntil = auth.SubscriptionAccountStateActive, nil
	next, present, err := runtime.Lease(ctx, testOwner, subscriptions.ProviderCodex, "session")
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, lease.AccountID, next.AccountID)
	require.Equal(t, lease.AccessToken, next.AccessToken)
	next.Release()
}

func TestCodexResetDoesNotSpendWhenAnyPersonalAccountHasHeadroom(t *testing.T) {
	runtime, _, provider, _, ctx := resetFixture(t)
	provider.exhausted["a"] = false
	lease, recovered, err := runtime.ResetCodex(ctx, testOwner, "session")
	require.NoError(t, err)
	require.True(t, recovered)
	require.Equal(t, "a", lease.AccountID)
	require.Empty(t, provider.redemptions)
	require.Zero(t, provider.inventoryReads)
}

func TestCodexResetUnknownQuotaNeverSpends(t *testing.T) {
	runtime, _, provider, _, ctx := resetFixture(t)
	provider.quotaErr = errors.New("quota unavailable")
	_, recovered, err := runtime.ResetCodex(ctx, testOwner, "session")
	require.Error(t, err)
	require.False(t, recovered)
	require.Empty(t, provider.redemptions)
}

func TestCodexResetSkipsExpiredAndOtherOwnersCredits(t *testing.T) {
	runtime, accounts, provider, _, ctx := resetFixture(t)
	accounts.accounts[1].SubscriberID = "different-person"
	provider.credits["a"] = []subscriptions.ResetCredit{{ID: "expired", ExpiresAt: time.Now().Add(-time.Second)}}
	_, recovered, err := runtime.ResetCodex(ctx, testOwner, "session")
	require.NoError(t, err)
	require.False(t, recovered)
	require.Equal(t, 1, provider.quotaReads)
	require.Equal(t, 1, provider.inventoryReads)
	require.Empty(t, provider.redemptions)
}

func TestCodexResetRetriesUncertainCreditWithSameKey(t *testing.T) {
	runtime, _, provider, store, ctx := resetFixture(t)
	provider.consumeErr = context.DeadlineExceeded
	_, recovered, err := runtime.ResetCodex(ctx, testOwner, "session")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, recovered)
	pending := provider.redemptions[0]
	require.Equal(t, pending.request, store.claim.RequestID)
	// Even if another credit now expires sooner, resume the uncertain operation.
	provider.consumeErr = nil
	provider.credits["a"] = []subscriptions.ResetCredit{{ID: "new-earliest", ExpiresAt: time.Now().Add(time.Minute)}}
	_, recovered, err = runtime.ResetCodex(ctx, testOwner, "session")
	require.NoError(t, err)
	require.True(t, recovered)
	require.Len(t, provider.redemptions, 2)
	require.Equal(t, pending, provider.redemptions[1])
}

func TestCodexResetRetainsSelectionUntilQuotaRestored(t *testing.T) {
	runtime, _, provider, store, ctx := resetFixture(t)
	provider.stayExhausted = true
	_, recovered, err := runtime.ResetCodex(ctx, testOwner, "session")
	require.NoError(t, err)
	require.False(t, recovered)
	require.NotEmpty(t, store.claim.CreditID)
	_, _, err = runtime.ResetCodex(ctx, testOwner, "session")
	require.NoError(t, err)
	require.Equal(t, provider.redemptions[0], provider.redemptions[1])
}

func TestCodexResetConcurrentReplicasConsumeOneCredit(t *testing.T) {
	runtime, accounts, provider, store, ctx := resetFixture(t)
	provider.started, provider.resume = make(chan struct{}), make(chan struct{})
	peer := subscriptions.NewRuntime(accounts, nil, nil).WithCodexResets(provider, store)
	finished := make(chan error, 1)
	go func() { _, _, err := runtime.ResetCodex(ctx, testOwner, "first"); finished <- err }()
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("reset did not start")
	}
	_, recovered, err := peer.ResetCodex(ctx, testOwner, "second")
	require.NoError(t, err)
	require.False(t, recovered)
	close(provider.resume)
	require.NoError(t, <-finished)
	require.Len(t, provider.redemptions, 1)
}

func TestCodexResetRefreshesExpiredTokenWithoutReactivatingQuota(t *testing.T) {
	_, accounts, provider, store, ctx := resetFixture(t)
	accounts.accessExpiry["b"] = time.Now().Add(-time.Hour)
	runtime := subscriptions.NewRuntime(accounts, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		return subscriptions.RefreshedToken{AccessToken: "renewed", RefreshToken: "rotated", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}), nil).WithCodexResets(provider, store)
	provider.stayExhausted = true
	_, recovered, err := runtime.ResetCodex(ctx, testOwner, "session")
	require.NoError(t, err)
	require.False(t, recovered)
	require.Equal(t, []byte("renewed"), accounts.accessTokens["b"])
	require.Equal(t, auth.SubscriptionAccountStateExhausted, accounts.accounts[1].State)
	require.NotNil(t, accounts.accounts[1].CooldownUntil)
}
