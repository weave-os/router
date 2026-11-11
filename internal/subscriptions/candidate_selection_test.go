package subscriptions_test

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/subscriptions"
)

func TestRuntimeDistributesEligibleCandidates(t *testing.T) {
	for _, provider := range []subscriptions.Provider{subscriptions.ProviderClaude, subscriptions.ProviderCodex} {
		for _, subscriberID := range []string{"subscriber-1", "subscriber-2"} {
			t.Run(string(provider)+"/"+subscriberID, func(t *testing.T) {
				for _, scenario := range []struct {
					name       string
					observed   bool
					holdLeases bool
				}{
					{"all_unknown", false, false},
					{"known_headroom_vs_unknown", true, false},
					{"concurrent_inflight", true, true},
				} {
					t.Run(scenario.name, func(t *testing.T) {
						now := time.Date(2026, time.November, 10, 12, 0, 0, 0, time.UTC)
						store := newRuntimeStore(
							&auth.SubscriptionAccount{ID: "first", SubscriberID: subscriberID, EnrolledByAPIKeyID: "key-1", Provider: auth.SubscriptionProvider(provider), Enabled: true},
							&auth.SubscriptionAccount{ID: "second", SubscriberID: subscriberID, EnrolledByAPIKeyID: "key-1", Provider: auth.SubscriptionProvider(provider), Enabled: true},
						)
						store.refreshTokens["first"] = []byte("first-refresh")
						store.refreshTokens["second"] = []byte("second-refresh")
						runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(_ context.Context, _ subscriptions.Provider, token string) (subscriptions.RefreshedToken, error) {
							return subscriptions.RefreshedToken{AccessToken: "access-" + token, RefreshToken: token, ExpiresAt: now.Add(time.Hour)}, nil
						}), func() time.Time { return now })
						runtime.WithCandidateSignal(func(id string, _ subscriptions.Provider, _ time.Time) subscriptions.CandidateSignal {
							if scenario.observed && (id == "first" || scenario.holdLeases) {
								return subscriptions.CandidateSignal{Observed: true, UsedFraction: 0.3, ResetAt: now.Add(time.Hour)}
							}
							return subscriptions.CandidateSignal{}
						})
						counts := map[string]int{}
						for i := 0; i < 10; i++ {
							lease, present, err := runtime.Lease(context.Background(), testOwner, provider, "sticky-session")
							require.NoError(t, err)
							require.True(t, present)
							counts[lease.AccountID]++
							if scenario.holdLeases {
								defer lease.Release()
							} else {
								lease.Release()
							}
						}
						require.Positive(t, counts["second"], "eligible unused capacity must receive traffic")
						if scenario.holdLeases {
							require.Equal(t, 5, counts["first"])
							require.Equal(t, 5, counts["second"])
						}
						t.Logf("observed selections: first=%d second=%d", counts["first"], counts["second"])
					})
				}
			})
		}
	}
}

func TestRuntimeExplorationSkipsIneligibleAccounts(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	store := newRuntimeStore(
		&auth.SubscriptionAccount{ID: "healthy", SubscriberID: "subscriber-1", Provider: auth.SubscriptionProviderClaude, Enabled: true},
		&auth.SubscriptionAccount{ID: "cooling", SubscriberID: "subscriber-1", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateCooldown, CooldownUntil: &future},
		&auth.SubscriptionAccount{ID: "disabled", SubscriberID: "subscriber-1", Provider: auth.SubscriptionProviderClaude, Enabled: false},
		&auth.SubscriptionAccount{ID: "excluded", SubscriberID: "subscriber-1", Provider: auth.SubscriptionProviderClaude, Enabled: true},
		&auth.SubscriptionAccount{ID: "other-provider", SubscriberID: "subscriber-1", Provider: auth.SubscriptionProviderCodex, Enabled: true},
	)
	store.refreshTokens["healthy"] = []byte("synthetic-refresh")
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(_ context.Context, _ subscriptions.Provider, token string) (subscriptions.RefreshedToken, error) {
		return subscriptions.RefreshedToken{AccessToken: "synthetic-access", RefreshToken: token, ExpiresAt: future}, nil
	}), func() time.Time { return now })
	runtime.WithCandidateSignal(func(id string, _ subscriptions.Provider, _ time.Time) subscriptions.CandidateSignal {
		if id == "healthy" {
			return subscriptions.CandidateSignal{Observed: true, ResetAt: future}
		}
		return subscriptions.CandidateSignal{}
	})
	owner := testOwner
	owner.ExcludedAccountIDs = []string{"excluded"}
	for range 30 {
		lease, _, err := runtime.Lease(t.Context(), owner, subscriptions.ProviderClaude, "")
		require.NoError(t, err)
		require.Equal(t, "healthy", lease.AccountID)
		lease.Release()
	}
}

func TestRuntimeReleasesReservationAfterCredentialFailure(t *testing.T) {
	now := time.Now()
	store := newRuntimeStore(
		&auth.SubscriptionAccount{ID: "first", SubscriberID: "subscriber-1", Provider: auth.SubscriptionProviderClaude, Enabled: true},
		&auth.SubscriptionAccount{ID: "second", SubscriberID: "subscriber-1", Provider: auth.SubscriptionProviderClaude, Enabled: true},
	)
	store.refreshTokens["first"] = []byte("refresh-first")
	store.refreshTokens["second"] = []byte("refresh-second")
	calls := 0
	runtime := subscriptions.NewRuntime(store, runtimeRefreshFunc(func(_ context.Context, _ subscriptions.Provider, token string) (subscriptions.RefreshedToken, error) {
		calls++
		if calls == 1 {
			return subscriptions.RefreshedToken{}, context.Canceled
		}
		return subscriptions.RefreshedToken{AccessToken: "synthetic-access", RefreshToken: token, ExpiresAt: now.Add(time.Hour)}, nil
	}), func() time.Time { return now })
	runtime.WithCandidateSignal(func(_ string, _ subscriptions.Provider, _ time.Time) subscriptions.CandidateSignal {
		return subscriptions.CandidateSignal{Observed: true, ResetAt: now.Add(time.Hour)}
	})
	_, _, err := runtime.Lease(t.Context(), testOwner, subscriptions.ProviderClaude, "")
	require.ErrorIs(t, err, context.Canceled)
	first, _, err := runtime.Lease(t.Context(), testOwner, subscriptions.ProviderClaude, "")
	require.NoError(t, err)
	require.Equal(t, "first", first.AccountID, "the failed credential lease must not retain load")
	first.Release()
	first.Release()
	held, _, err := runtime.Lease(t.Context(), testOwner, subscriptions.ProviderClaude, "")
	require.NoError(t, err)
	defer held.Release()
	require.Equal(t, "first", held.AccountID)
	next, _, err := runtime.Lease(t.Context(), testOwner, subscriptions.ProviderClaude, "")
	require.NoError(t, err)
	defer next.Release()
	require.Equal(t, "second", next.AccountID, "double release must not let a busy account report negative load")
}
