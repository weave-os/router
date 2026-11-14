package subscriptions_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/subscriptions"
)

const simulatedDay = 24 * time.Hour

// The issuer advertises a longer access lifetime than its session accepts.
// These scenarios test the refresh mechanism, not OpenAI's actual policy.
type weeklyCodexIssuer struct {
	t            *testing.T
	mu           sync.Mutex
	clock        func() time.Time
	hardDeadline time.Time
	refreshUntil map[string]time.Time
	accessUntil  map[string]time.Time
	refreshes    int
}

func (i *weeklyCodexIssuer) Refresh(_ context.Context, _ subscriptions.Provider, refresh string) (subscriptions.RefreshedToken, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.clock().Before(i.refreshUntil[refresh]) {
		return subscriptions.RefreshedToken{}, &subscriptions.OAuthRefreshError{
			Provider: subscriptions.ProviderCodex, Status: http.StatusUnauthorized,
			Code: "refresh_token_invalidated", ErrorType: "invalid_request_error",
		}
	}
	i.refreshes++
	access, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(i.clock()), ID: fmt.Sprint(i.refreshes)}).SignedString([]byte("synthetic-test-signing-key"))
	require.NoError(i.t, err)
	nextRefresh := fmt.Sprintf("synthetic-refresh-%d", i.refreshes)
	deadline := i.clock().Add(7 * simulatedDay)
	if !i.hardDeadline.IsZero() && i.hardDeadline.Before(deadline) {
		deadline = i.hardDeadline
	}
	delete(i.refreshUntil, refresh)
	i.refreshUntil[nextRefresh] = deadline
	i.accessUntil[access] = deadline
	return subscriptions.RefreshedToken{AccessToken: access, RefreshToken: nextRefresh,
		AccountID: "synthetic-workspace", ExpiresAt: i.clock().Add(10 * simulatedDay)}, nil
}

func (i *weeklyCodexIssuer) accepts(access string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.clock().Before(i.accessUntil[access])
}

type proactiveRefreshFixture struct {
	now     atomic.Int64
	started time.Time
	store   *runtimeStore
	issuer  *weeklyCodexIssuer
	runtime *subscriptions.Runtime
}

func newProactiveRefreshFixture(t *testing.T, hardSession, earlyRefresh bool) *proactiveRefreshFixture {
	f := &proactiveRefreshFixture{started: time.Now().UTC()}
	f.now.Store(f.started.UnixNano())
	clock := func() time.Time { return time.Unix(0, f.now.Load()).UTC() }
	f.issuer = &weeklyCodexIssuer{t: t, clock: clock,
		refreshUntil: map[string]time.Time{"synthetic-seed": f.started.Add(7 * simulatedDay)},
		accessUntil:  make(map[string]time.Time)}
	if hardSession {
		f.issuer.hardDeadline = f.started.Add(7 * simulatedDay)
	}
	f.store = newRuntimeStore(&auth.SubscriptionAccount{ID: "synthetic-seat", SubscriberID: testOwner.SubscriberID,
		EnrolledByAPIKeyID: testOwner.APIKeyID, Provider: auth.SubscriptionProviderCodex,
		ExternalAccountID: "synthetic-workspace", Enabled: true})
	f.store.clock = clock
	f.store.refreshTokens["synthetic-seat"] = []byte("synthetic-seed")
	f.runtime = subscriptions.NewRuntime(f.store, f.issuer, clock)
	if earlyRefresh {
		policy, err := subscriptions.NewEarlyRefreshPolicy([]string{"synthetic-seat"}, 6*simulatedDay)
		require.NoError(t, err)
		f.runtime.WithEarlyRefreshPolicy(policy)
	}
	return f
}

func (f *proactiveRefreshFixture) accessAt(t *testing.T, age time.Duration) string {
	t.Helper()
	f.now.Store(f.started.Add(age).UnixNano())
	lease, present, err := f.runtime.Lease(context.Background(), testOwner, subscriptions.ProviderCodex, "synthetic-session")
	require.NoError(t, err)
	require.True(t, present)
	defer lease.Release()
	return lease.AccessToken
}

func TestProactiveRefreshBeforeWeeklySessionBoundary(t *testing.T) {
	for _, hardSession := range []bool{false, true} {
		for _, earlyRefresh := range []bool{false, true} {
			t.Run(fmt.Sprintf("hard_session=%t/early_refresh=%t", hardSession, earlyRefresh), func(t *testing.T) {
				f := newProactiveRefreshFixture(t, hardSession, earlyRefresh)
				require.True(t, f.issuer.accepts(f.accessAt(t, 0)))
				require.True(t, f.issuer.accepts(f.accessAt(t, 6*simulatedDay)))
				accepted := f.issuer.accepts(f.accessAt(t, 7*simulatedDay))
				require.Equal(t, earlyRefresh && !hardSession, accepted)
				if earlyRefresh {
					require.Equal(t, 2, f.issuer.refreshes)
					require.Equal(t, int64(2), f.store.tokenRefreshVersions["synthetic-seat"])
				} else {
					require.Equal(t, 1, f.issuer.refreshes, "reported expiry alone leaves the old access token cached")
				}
				if hardSession {
					_, err := f.runtime.RefreshAccount(context.Background(), testOwner, "synthetic-seat")
					var rejected *subscriptions.OAuthRefreshError
					require.ErrorAs(t, err, &rejected)
					require.Equal(t, "refresh_token_invalidated", rejected.Code)
				}
			})
		}
	}
}

func TestProactiveRefreshPersistsRotationBeforeReusingCredential(t *testing.T) {
	f := newProactiveRefreshFixture(t, false, true)
	f.accessAt(t, 0)
	f.store.persistFailures = 1
	access := f.accessAt(t, 6*simulatedDay)
	require.Equal(t, 2, f.issuer.refreshes, "retry storage without spending the old refresh token again")
	require.Equal(t, 3, f.store.persistCalls)
	require.Equal(t, []byte("synthetic-refresh-2"), f.store.refreshTokens["synthetic-seat"])
	require.Equal(t, []byte(access), f.store.accessTokens["synthetic-seat"])
	require.True(t, f.issuer.accepts(f.accessAt(t, 7*simulatedDay)))
}
