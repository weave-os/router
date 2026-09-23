package auth_test

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"

	"weave-os/router/internal/auth"

	"github.com/stretchr/testify/require"
)

const (
	sharedKeyID     = "44444444-4444-4444-4444-444444444444"
	installationOne = "55555555-5555-5555-5555-555555555555"
	installationTwo = "66666666-6666-6666-6666-666666666666"
	keyOwnerSubject = "77777777-7777-7777-7777-777777777777"
	aliSubject      = "88888888-8888-8888-8888-888888888888"
	samSubject      = "99999999-9999-9999-9999-999999999999"
)

// fakeRequestIdentities stands in for Weave's projection: an address resolves
// only inside the installation it was projected into.
type fakeRequestIdentities struct {
	projected map[string]map[string]string
	err       error
	lookups   atomic.Int64
}

func (f *fakeRequestIdentities) GetSubscriberForEmail(_ context.Context, installationID, email string) (string, error) {
	f.lookups.Add(1)
	if f.err != nil {
		return "", f.err
	}
	subscriberID, projected := f.projected[installationID][email]
	if !projected {
		return "", sql.ErrNoRows
	}
	return subscriberID, nil
}

func requestIdentityService(repo auth.RequestIdentityRepository) *auth.Service {
	return auth.NewService(nil, nil, nil, nil, auth.NoOpAPIKeyCache{}, nil, frozenClock()).
		WithRequestIdentities(repo)
}

func sharedKey() *auth.APIKey {
	return &auth.APIKey{ID: sharedKeyID, InstallationID: installationOne, CredentialSubjectID: keyOwnerSubject}
}

func teamProjection() *fakeRequestIdentities {
	return &fakeRequestIdentities{projected: map[string]map[string]string{
		installationOne: {"ali@weave.test": aliSubject, "sam@weave.test": samSubject},
		installationTwo: {"outsider@other.test": samSubject},
	}}
}

func TestSubscriptionOwnerForRequestSeparatesCallersSharingAKey(t *testing.T) {
	svc := requestIdentityService(teamProjection())
	key := sharedKey()

	ali, err := svc.SubscriptionOwnerForRequest(context.Background(), key, "ali@weave.test")
	require.NoError(t, err)
	sam, err := svc.SubscriptionOwnerForRequest(context.Background(), key, "sam@weave.test")
	require.NoError(t, err)

	require.Equal(t, aliSubject, ali.SubscriberID)
	require.Equal(t, samSubject, sam.SubscriberID)
	require.NotEqual(t, ali.PoolKey(), sam.PoolKey())
	// Attribution of the row still needs the key that enrolled it.
	require.Equal(t, sharedKeyID, ali.APIKeyID)
	// The cached key object is shared by every concurrent caller, so resolving
	// an identity must not write the person onto it.
	require.Equal(t, keyOwnerSubject, key.CredentialSubjectID)
}

func TestSubscriptionOwnerForRequestUncachedSeesAWithdrawnProjection(t *testing.T) {
	repo := teamProjection()
	svc := requestIdentityService(repo)
	key := sharedKey()

	owner, err := svc.SubscriptionOwnerForRequest(context.Background(), key, "ali@weave.test")
	require.NoError(t, err)
	require.Equal(t, aliSubject, owner.SubscriberID)

	delete(repo.projected[installationOne], "ali@weave.test")

	cached, err := svc.SubscriptionOwnerForRequest(context.Background(), key, "ali@weave.test")
	require.NoError(t, err)
	require.Equal(t, aliSubject, cached.SubscriberID)

	// Managing a linked account reads the projection as it stands, so the
	// withdrawn address stops naming Ali's pool straight away.
	live, err := svc.SubscriptionOwnerForRequestUncached(context.Background(), key, "ali@weave.test")
	require.NoError(t, err)
	require.Equal(t, keyOwnerSubject, live.SubscriberID)
}

func TestSubscriptionOwnerForRequestFallsBackToTheKeysOwner(t *testing.T) {
	svc := requestIdentityService(teamProjection())

	for _, testCase := range []struct {
		name  string
		email string
	}{
		{name: "no email sent", email: ""},
		{name: "address nobody projected", email: "stranger@weave.test"},
		// An address belonging to another installation must not reach across:
		// resolution is scoped to the installation the key authenticated into.
		{name: "address from another installation", email: "outsider@other.test"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			owner, err := svc.SubscriptionOwnerForRequest(context.Background(), sharedKey(), testCase.email)
			require.NoError(t, err)
			require.Equal(t, keyOwnerSubject, owner.SubscriberID)
		})
	}
}

func TestSubscriptionOwnerForRequestReportsLookupFailureWithoutLosingTheKeysOwner(t *testing.T) {
	svc := requestIdentityService(&fakeRequestIdentities{err: sql.ErrConnDone})

	owner, err := svc.SubscriptionOwnerForRequest(context.Background(), sharedKey(), "ali@weave.test")
	require.ErrorIs(t, err, sql.ErrConnDone)
	require.Equal(t, keyOwnerSubject, owner.SubscriberID)
}

func TestSubscriptionOwnerForRequestWithoutProjectionKeepsKeyOwnership(t *testing.T) {
	svc := auth.NewService(nil, nil, nil, nil, auth.NoOpAPIKeyCache{}, nil, frozenClock())

	owner, err := svc.SubscriptionOwnerForRequest(context.Background(), sharedKey(), "ali@weave.test")
	require.NoError(t, err)
	require.Equal(t, keyOwnerSubject, owner.SubscriberID)
}

func TestSubscriptionOwnerForRequestResolvesConcurrentCallersIndependently(t *testing.T) {
	repo := teamProjection()
	svc := requestIdentityService(repo)
	key := sharedKey()

	resolved := make([]string, 64)
	var callers sync.WaitGroup
	for i := range resolved {
		callers.Add(1)
		go func() {
			defer callers.Done()
			email := "ali@weave.test"
			if i%2 == 1 {
				email = "sam@weave.test"
			}
			owner, err := svc.SubscriptionOwnerForRequest(context.Background(), key, email)
			require.NoError(t, err)
			resolved[i] = owner.SubscriberID
		}()
	}
	callers.Wait()

	for i, subscriberID := range resolved {
		expected := aliSubject
		if i%2 == 1 {
			expected = samSubject
		}
		require.Equal(t, expected, subscriberID)
	}
	// The per-turn lookup is cached, so a shared key's traffic doesn't put the
	// projection table on the hot path.
	require.Less(t, repo.lookups.Load(), int64(len(resolved)))
}
