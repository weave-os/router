package auth_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"weave-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeUserRepo struct {
	mu             sync.Mutex
	upserts        []auth.UpsertUserParams
	accountUpserts []auth.UpsertUserByAccountUUIDParams
	user           *auth.User
	err            error
}

func (f *fakeUserRepo) UpsertByEmail(ctx context.Context, params auth.UpsertUserParams) (*auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, params)
	if f.err != nil {
		return nil, f.err
	}
	return f.user, nil
}

func (f *fakeUserRepo) UpsertByAccountUUID(ctx context.Context, params auth.UpsertUserByAccountUUIDParams) (*auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accountUpserts = append(f.accountUpserts, params)
	if f.err != nil {
		return nil, f.err
	}
	return f.user, nil
}

func (f *fakeUserRepo) Get(ctx context.Context, id string) (*auth.User, error) {
	return nil, errors.New("not used by these tests")
}

func (f *fakeUserRepo) ListForInstallation(ctx context.Context, installationID string) ([]*auth.User, error) {
	return nil, errors.New("not used by these tests")
}

func makeServiceWithUsers(t *testing.T, users auth.UserRepository) *auth.Service {
	t.Helper()
	return auth.NewService(
		&fakeInstallationRepository{},
		&fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{}},
		nil,
		users,
		auth.NoOpAPIKeyCache{},
		nil,
		frozenClock(),
	)
}

func TestResolveAndStashUser_UpsertsAndStashesID(t *testing.T) {
	repo := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	svc := makeServiceWithUsers(t, repo)

	ctx := svc.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "claude-acct-9", "")

	require.Len(t, repo.upserts, 1)
	assert.Equal(t, "inst-1", repo.upserts[0].InstallationID)
	assert.Equal(t, "alice@example.com", repo.upserts[0].Email)
	require.NotNil(t, repo.upserts[0].ClaudeAccountUUID)
	assert.Equal(t, "claude-acct-9", *repo.upserts[0].ClaudeAccountUUID)
	assert.Equal(t, "user-42", auth.UserIDFrom(ctx))
}

func TestResolveAndStashUser_NoIdentitySignalIsNoOp(t *testing.T) {
	repo := &fakeUserRepo{}
	svc := makeServiceWithUsers(t, repo)

	ctx := svc.ResolveAndStashUser(context.Background(), "inst-1", "", "", "")

	assert.Empty(t, repo.upserts)
	assert.Empty(t, repo.accountUpserts)
	assert.Equal(t, "", auth.UserIDFrom(ctx))
}

func TestResolveAndStashUser_AccountUUIDOnlyUsesAccountUpsert(t *testing.T) {
	// Claude CLI v2.1.x packs only {device_id, account_uuid, session_id}
	// into metadata.user_id — no email. Per-seat attribution must still
	// work via the account_uuid-keyed upsert path.
	repo := &fakeUserRepo{user: &auth.User{ID: "user-9", InstallationID: "inst-1"}}
	svc := makeServiceWithUsers(t, repo)

	ctx := svc.ResolveAndStashUser(context.Background(), "inst-1", "", "2c2aace8-82e9-4cb1-8d1f-2f822da43177", "")

	assert.Empty(t, repo.upserts, "email-empty input must NOT call UpsertByEmail")
	require.Len(t, repo.accountUpserts, 1)
	assert.Equal(t, "inst-1", repo.accountUpserts[0].InstallationID)
	assert.Equal(t, "2c2aace8-82e9-4cb1-8d1f-2f822da43177", repo.accountUpserts[0].ClaudeAccountUUID)
	assert.Equal(t, "user-9", auth.UserIDFrom(ctx))
}

func TestResolveAndStashUser_EmailPathBeatsAccountUUIDPath(t *testing.T) {
	// When both signals are present, email is the canonical key and the
	// account_uuid rides along as enrichment on the email-keyed row.
	// Using UpsertByAccountUUID here would create a duplicate seat.
	repo := &fakeUserRepo{user: &auth.User{ID: "user-3"}}
	svc := makeServiceWithUsers(t, repo)

	svc.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "2c2aace8-82e9-4cb1-8d1f-2f822da43177", "")

	require.Len(t, repo.upserts, 1)
	assert.Empty(t, repo.accountUpserts, "email-present input must NOT call UpsertByAccountUUID")
	require.NotNil(t, repo.upserts[0].ClaudeAccountUUID)
	assert.Equal(t, "2c2aace8-82e9-4cb1-8d1f-2f822da43177", *repo.upserts[0].ClaudeAccountUUID)
}

func TestResolveAndStashUser_NoInstallationIsNoOp(t *testing.T) {
	repo := &fakeUserRepo{}
	svc := makeServiceWithUsers(t, repo)

	ctx := svc.ResolveAndStashUser(context.Background(), "", "alice@example.com", "", "")

	assert.Empty(t, repo.upserts)
	assert.Equal(t, "", auth.UserIDFrom(ctx))
}

func TestResolveAndStashUser_OmitsClaudeAccountWhenEmpty(t *testing.T) {
	repo := &fakeUserRepo{user: &auth.User{ID: "user-1"}}
	svc := makeServiceWithUsers(t, repo)

	svc.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")

	require.Len(t, repo.upserts, 1)
	assert.Nil(t, repo.upserts[0].ClaudeAccountUUID)
}

func TestResolveAndStashUser_PropagatesDisplayNameOnEmailPath(t *testing.T) {
	repo := &fakeUserRepo{user: &auth.User{ID: "user-1"}}
	svc := makeServiceWithUsers(t, repo)

	svc.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "Alice Liddell")

	require.Len(t, repo.upserts, 1)
	require.NotNil(t, repo.upserts[0].DisplayName)
	assert.Equal(t, "Alice Liddell", *repo.upserts[0].DisplayName)
}

func TestResolveAndStashUser_PropagatesDisplayNameOnAccountUUIDPath(t *testing.T) {
	// Claude CLI v2.1.x ships only account_uuid in metadata.user_id, but the
	// X-Weave-User-Name header still carries the git user.name. The display
	// name must reach the account-uuid-keyed upsert so the dashboard has a
	// human-readable label even when email is NULL.
	repo := &fakeUserRepo{user: &auth.User{ID: "user-9"}}
	svc := makeServiceWithUsers(t, repo)

	svc.ResolveAndStashUser(context.Background(), "inst-1", "", "2c2aace8-82e9-4cb1-8d1f-2f822da43177", "Alice Liddell")

	require.Len(t, repo.accountUpserts, 1)
	require.NotNil(t, repo.accountUpserts[0].DisplayName)
	assert.Equal(t, "Alice Liddell", *repo.accountUpserts[0].DisplayName)
}

func TestResolveAndStashUser_OmitsDisplayNameWhenEmpty(t *testing.T) {
	repo := &fakeUserRepo{user: &auth.User{ID: "user-1"}}
	svc := makeServiceWithUsers(t, repo)

	svc.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")

	require.Len(t, repo.upserts, 1)
	assert.Nil(t, repo.upserts[0].DisplayName, "empty header must map to nil so COALESCE preserves any existing row value")
}

func TestResolveAndStashUser_RepoErrorDoesNotPropagate(t *testing.T) {
	repo := &fakeUserRepo{err: errors.New("db down")}
	svc := makeServiceWithUsers(t, repo)

	// Must return the original ctx unchanged so the request still proceeds.
	ctx := svc.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")

	assert.Equal(t, "", auth.UserIDFrom(ctx))
}

func TestResolveAndStashUser_NilUsersIsNoOp(t *testing.T) {
	svc := makeServiceWithUsers(t, nil)

	ctx := svc.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")

	assert.Equal(t, "", auth.UserIDFrom(ctx))
}

func TestResolveAndStashUser_CacheHitSkipsRepo(t *testing.T) {
	repo := &fakeUserRepo{user: &auth.User{ID: "user-1"}}
	cache := auth.NewLRUUserCache(8, 5*time.Minute)
	svc := auth.NewService(
		&fakeInstallationRepository{},
		&fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{}},
		nil,
		repo,
		auth.NoOpAPIKeyCache{},
		cache,
		frozenClock(),
	)

	// First call hits repo and populates cache.
	ctx1 := svc.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")
	require.Equal(t, "user-1", auth.UserIDFrom(ctx1))
	require.Len(t, repo.upserts, 1)

	// Second call must hit cache and skip the upsert entirely.
	ctx2 := svc.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")
	assert.Equal(t, "user-1", auth.UserIDFrom(ctx2))
	assert.Len(t, repo.upserts, 1, "cache hit must not call repo.Upsert again")
}

func TestLRUUserCache_KeysIncludeInstallation(t *testing.T) {
	cache := auth.NewLRUUserCache(8, time.Minute)
	cache.Set("inst-A", "alice@example.com", "user-1")
	cache.Set("inst-B", "alice@example.com", "user-2")

	got, ok := cache.Get("inst-A", "alice@example.com")
	require.True(t, ok)
	assert.Equal(t, "user-1", got)

	got, ok = cache.Get("inst-B", "alice@example.com")
	require.True(t, ok)
	assert.Equal(t, "user-2", got)

	_, ok = cache.Get("inst-C", "alice@example.com")
	assert.False(t, ok, "unrelated installation must miss")
}

type fakeUserClusterListRepo struct {
	mu    sync.Mutex
	calls int
	lists []auth.UserClusterModelList
	err   error
}

func (f *fakeUserClusterListRepo) GetForUser(ctx context.Context, routerUserID string) ([]auth.UserClusterModelList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.lists, nil
}

func (f *fakeUserClusterListRepo) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestResolveAndStashUser_StashesUserClusterLists(t *testing.T) {
	users := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	clusters := &fakeUserClusterListRepo{lists: []auth.UserClusterModelList{
		{RouterUserID: "user-42", ClusterLabel: "balanced", Models: []string{"a", "b"}},
	}}
	svc := makeServiceWithUsers(t, users).
		WithUserClusterModelLists(clusters, auth.NewLRUUserClusterListCache(10, time.Minute))

	ctx := svc.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")

	assert.Equal(t, "user-42", auth.UserIDFrom(ctx))
	assert.Equal(t, map[string][]string{"balanced": {"a", "b"}}, auth.UserClusterModelListsFrom(ctx))
}

// The user-identity cache short-circuits before the upsert, so the selection
// load must happen on that path too — otherwise a warm cache silently drops
// every user's per-cluster selection.
func TestResolveAndStashUser_StashesClusterListsOnIdentityCacheHit(t *testing.T) {
	users := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	clusters := &fakeUserClusterListRepo{lists: []auth.UserClusterModelList{
		{RouterUserID: "user-42", ClusterLabel: "fast", Models: []string{"haiku"}},
	}}
	svc := auth.NewService(
		&fakeInstallationRepository{},
		&fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{}},
		nil,
		users,
		auth.NoOpAPIKeyCache{},
		auth.NewLRUUserCache(10, time.Minute),
		frozenClock(),
	).WithUserClusterModelLists(clusters, auth.NewLRUUserClusterListCache(10, time.Minute))

	// First call populates the identity cache.
	_ = svc.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")
	require.Len(t, users.upserts, 1)

	ctx := svc.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")

	assert.Len(t, users.upserts, 1, "second call must hit the identity cache")
	assert.Equal(t, "user-42", auth.UserIDFrom(ctx))
	assert.Equal(t, map[string][]string{"fast": {"haiku"}}, auth.UserClusterModelListsFrom(ctx),
		"the cache-hit path must still carry the user's cluster selections")
}

// Fail-open: a transient DB error serves the request on default routing rather
// than failing an authenticated request, and must not be cached.
func TestResolveAndStashUser_ClusterListFetchErrorFailsOpenAndIsNotCached(t *testing.T) {
	users := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	clusters := &fakeUserClusterListRepo{err: errors.New("db down")}
	svc := makeServiceWithUsers(t, users).
		WithUserClusterModelLists(clusters, auth.NewLRUUserClusterListCache(10, time.Minute))

	ctx := svc.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")

	assert.Equal(t, "user-42", auth.UserIDFrom(ctx), "the request must still be served")
	assert.Nil(t, auth.UserClusterModelListsFrom(ctx))

	_ = svc.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")
	assert.Equal(t, 2, clusters.callCount(), "an errored fetch must not be cached")
}

// A successful empty result IS cached — the common case must not re-query.
func TestResolveAndStashUser_EmptyClusterListsAreCached(t *testing.T) {
	users := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	clusters := &fakeUserClusterListRepo{}
	svc := makeServiceWithUsers(t, users).
		WithUserClusterModelLists(clusters, auth.NewLRUUserClusterListCache(10, time.Minute))

	_ = svc.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")
	ctx := svc.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")

	assert.Nil(t, auth.UserClusterModelListsFrom(ctx))
	assert.Equal(t, 1, clusters.callCount(), "an empty result must be cached, not re-fetched")
}

// Unwired repo (the default) must behave exactly as before.
func TestResolveAndStashUser_NoClusterRepoStashesNothing(t *testing.T) {
	users := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	svc := makeServiceWithUsers(t, users)

	ctx := svc.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")

	assert.Equal(t, "user-42", auth.UserIDFrom(ctx))
	assert.Nil(t, auth.UserClusterModelListsFrom(ctx))
}
