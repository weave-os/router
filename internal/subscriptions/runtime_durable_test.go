package subscriptions_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/subscriptions"
)

// durableStore applies cooldown writes to the account rows it lists, as the
// database does, so a second Runtime reading the same store sees them.
type durableStore struct {
	*runtimeStore
}

func (s *durableStore) UpdateSubscriptionAccountCooldown(ctx context.Context, owner auth.SubscriptionOwner, accountID string, cooldownUntil time.Time) error {
	if err := s.runtimeStore.UpdateSubscriptionAccountCooldown(ctx, owner, accountID, cooldownUntil); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, account := range s.accounts {
		if account.ID == accountID {
			until := cooldownUntil
			account.CooldownUntil = &until
			account.State = auth.SubscriptionAccountStateCooldown
		}
	}
	return nil
}

func (s *durableStore) setRow(accountID string, state auth.SubscriptionAccountState, cooldownUntil *time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, account := range s.accounts {
		if account.ID == accountID {
			account.State = state
			account.CooldownUntil = cooldownUntil
		}
	}
}

func newDurableRuntime(store *durableStore, clock func() time.Time) *subscriptions.Runtime {
	return subscriptions.NewRuntime(store, runtimeRefreshFunc(func(context.Context, subscriptions.Provider, string) (subscriptions.RefreshedToken, error) {
		return subscriptions.RefreshedToken{AccessToken: "access", RefreshToken: "refresh-secret", ExpiresAt: clock().Add(time.Hour)}, nil
	}), clock)
}

func durableAccount(until *time.Time, state auth.SubscriptionAccountState) *auth.SubscriptionAccount {
	return &auth.SubscriptionAccount{
		ID: "account-1", SubscriberID: "subscriber-1", EnrolledByAPIKeyID: "key-1",
		Provider: auth.SubscriptionProviderClaude, Enabled: true, State: state, CooldownUntil: until,
	}
}

// A cooldown cleared in the table must take effect on the very next lease,
// not after a sync interval and not after a restart.
func TestRuntimeLeaseFollowsTableWhenCooldownIsCleared(t *testing.T) {
	now := time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	until := now.Add(48 * time.Hour)
	store := &durableStore{newRuntimeStore(durableAccount(&until, auth.SubscriptionAccountStateCooldown))}
	store.refreshTokens["account-1"] = []byte("refresh-secret")
	runtime := newDurableRuntime(store, clock)

	_, _, err := runtime.Lease(context.Background(), testOwner, subscriptions.ProviderClaude, "")
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount)

	store.setRow("account-1", auth.SubscriptionAccountStateUnknown, nil)
	now = now.Add(time.Second)

	lease, present, err := runtime.Lease(context.Background(), testOwner, subscriptions.ProviderClaude, "")
	require.NoError(t, err)
	require.True(t, present)
	lease.Release()
}

// A cooldown whose time has passed in the table is honoured the same way.
func TestRuntimeLeaseFollowsTableWhenCooldownExpires(t *testing.T) {
	now := time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	until := now.Add(48 * time.Hour)
	store := &durableStore{newRuntimeStore(durableAccount(&until, auth.SubscriptionAccountStateCooldown))}
	store.refreshTokens["account-1"] = []byte("refresh-secret")
	runtime := newDurableRuntime(store, clock)

	_, _, err := runtime.Lease(context.Background(), testOwner, subscriptions.ProviderClaude, "")
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount)

	past := now.Add(-time.Minute)
	store.setRow("account-1", auth.SubscriptionAccountStateCooldown, &past)
	now = now.Add(time.Second)

	lease, _, err := runtime.Lease(context.Background(), testOwner, subscriptions.ProviderClaude, "")
	require.NoError(t, err)
	lease.Release()
}

// Two router replicas share one table: a cooldown recorded by one is
// honoured by the other on its next lease.
func TestRuntimeReplicasAgreeOnCooldown(t *testing.T) {
	now := time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := &durableStore{newRuntimeStore(durableAccount(nil, auth.SubscriptionAccountStateUnknown))}
	store.refreshTokens["account-1"] = []byte("refresh-secret")
	replicaA := newDurableRuntime(store, clock)
	replicaB := newDurableRuntime(store, clock)

	lease, _, err := replicaB.Lease(context.Background(), testOwner, subscriptions.ProviderClaude, "")
	require.NoError(t, err)
	lease.Release()

	leaseA, _, err := replicaA.Lease(context.Background(), testOwner, subscriptions.ProviderClaude, "")
	require.NoError(t, err)
	leaseA.Release()

	require.NoError(t, replicaA.Cooldown(context.Background(), testOwner, subscriptions.ProviderClaude, "account-1", now.Add(time.Hour)))
	now = now.Add(time.Second)

	_, _, err = replicaB.Lease(context.Background(), testOwner, subscriptions.ProviderClaude, "")
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount)
}
