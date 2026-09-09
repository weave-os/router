package auth_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"weave-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssignBlindExperimentArmBoundariesAndStability(t *testing.T) {
	assert.Equal(t, auth.BlindExperimentArmPassthrough, auth.AssignBlindExperimentArm("seed", "subject", 0))
	assert.Equal(t, auth.BlindExperimentArmRouterOn, auth.AssignBlindExperimentArm("seed", "subject", 100))

	first := auth.AssignBlindExperimentArm("seed", "subject", 50)
	for range 10 {
		assert.Equal(t, first, auth.AssignBlindExperimentArm("seed", "subject", 50))
	}
}

func TestAssignBlindExperimentArmSeedRotationChangesKnownSubject(t *testing.T) {
	assert.Equal(t, auth.BlindExperimentArmRouterOn, auth.AssignBlindExperimentArm("seed-a", "user-42", 50))
	assert.Equal(t, auth.BlindExperimentArmPassthrough, auth.AssignBlindExperimentArm("seed-b", "user-42", 50))
}

func TestAssignBlindExperimentArmPercentageOnlyMovesBoundarySubjects(t *testing.T) {
	movedIntoRouterOn := 0
	for index := range 500 {
		subject := "subject-" + strconv.Itoa(index)
		atTwentyFive := auth.AssignBlindExperimentArm("seed", subject, 25)
		atSeventyFive := auth.AssignBlindExperimentArm("seed", subject, 75)
		if atTwentyFive == auth.BlindExperimentArmRouterOn {
			assert.Equal(t, auth.BlindExperimentArmRouterOn, atSeventyFive)
		}
		if atTwentyFive == auth.BlindExperimentArmPassthrough && atSeventyFive == auth.BlindExperimentArmRouterOn {
			movedIntoRouterOn++
		}
	}
	assert.Positive(t, movedIntoRouterOn, "raising the percentage must expand the router-on cohort")
}

func TestLRUBlindExperimentCacheStoresInactiveAndInvalidatesByInstallation(t *testing.T) {
	cache := auth.NewLRUBlindExperimentCache(10, time.Minute, time.Now)
	firstGeneration := cache.InstallationGeneration("inst-1")
	secondGeneration := cache.InstallationGeneration("inst-2")
	require.True(t, cache.SetAtGeneration("inst-1", "user-1", auth.BlindExperimentState{}, firstGeneration))
	require.True(t, cache.SetAtGeneration("inst-2", "user-2", auth.BlindExperimentState{Active: true, Arm: auth.BlindExperimentArmRouterOn}, secondGeneration))

	_, inactiveHit := cache.GetAtGeneration("inst-1", "user-1", firstGeneration)
	require.True(t, inactiveHit, "a missing or disabled configuration must be negatively cached")

	cache.InvalidateInstallation("inst-1")
	_, firstFound := cache.GetAtGeneration("inst-1", "user-1", cache.InstallationGeneration("inst-1"))
	_, secondFound := cache.GetAtGeneration("inst-2", "user-2", secondGeneration)
	assert.False(t, firstFound)
	assert.True(t, secondFound)
}

func TestLRUBlindExperimentCacheReassignsUserBetweenInstallations(t *testing.T) {
	cache := auth.NewLRUBlindExperimentCache(10, time.Minute, time.Now)
	require.True(t, cache.SetAtGeneration("inst-1", "user-1", auth.BlindExperimentState{Active: true, Arm: auth.BlindExperimentArmRouterOn}, cache.InstallationGeneration("inst-1")))
	secondGeneration := cache.InstallationGeneration("inst-2")
	require.True(t, cache.SetAtGeneration("inst-2", "user-1", auth.BlindExperimentState{Active: true, Arm: auth.BlindExperimentArmPassthrough}, secondGeneration))

	cache.InvalidateInstallation("inst-1")
	state, found := cache.GetAtGeneration("inst-2", "user-1", secondGeneration)
	assert.True(t, found, "invalidating the previous installation must not evict the reassigned user")
	assert.Equal(t, auth.BlindExperimentArmPassthrough, state.Arm)

	cache.InvalidateInstallation("inst-2")
	_, found = cache.GetAtGeneration("inst-2", "user-1", cache.InstallationGeneration("inst-2"))
	assert.False(t, found)
}

func TestLRUBlindExperimentCacheTTLExpires(t *testing.T) {
	cache := auth.NewLRUBlindExperimentCache(10, 10*time.Millisecond, time.Now)
	generation := cache.InstallationGeneration("inst-1")
	require.True(t, cache.SetAtGeneration("inst-1", "user-1", auth.BlindExperimentState{Active: true, Arm: auth.BlindExperimentArmPassthrough}, generation))

	require.Eventually(t, func() bool {
		_, found := cache.GetAtGeneration("inst-1", "user-1", generation)
		return !found
	}, time.Second, 5*time.Millisecond)
}

func TestLRUBlindExperimentCacheBoundsErrorsSeparately(t *testing.T) {
	cache := auth.NewLRUBlindExperimentCache(1, time.Minute, time.Now)
	generation := cache.InstallationGeneration("inst-1")
	require.True(t, cache.SetAtGeneration("inst-1", "active-user", auth.BlindExperimentState{Active: true, Arm: auth.BlindExperimentArmRouterOn}, generation))
	require.True(t, cache.SetErrorAtGeneration("inst-1", "failed-user", generation))

	state, found := cache.GetAtGeneration("inst-1", "active-user", generation)
	assert.True(t, found, "a failed lookup must not evict an active assignment")
	assert.Equal(t, auth.BlindExperimentArmRouterOn, state.Arm)
	_, found = cache.GetAtGeneration("inst-1", "failed-user", generation)
	assert.True(t, found, "an active error entry should fail open without a repository retry")
}

func TestLRUBlindExperimentCacheRejectsStaleAssignmentsAndErrors(t *testing.T) {
	cache := auth.NewLRUBlindExperimentCache(10, time.Minute, time.Now)
	staleGeneration := cache.InstallationGeneration("inst-1")
	cache.InvalidateInstallation("inst-1")

	assert.False(t, cache.SetAtGeneration("inst-1", "user-1", auth.BlindExperimentState{Active: true, Arm: auth.BlindExperimentArmRouterOn}, staleGeneration))
	assert.False(t, cache.SetErrorAtGeneration("inst-1", "user-2", staleGeneration))
	currentGeneration := cache.InstallationGeneration("inst-1")
	_, assignmentFound := cache.GetAtGeneration("inst-1", "user-1", currentGeneration)
	_, errorFound := cache.GetAtGeneration("inst-1", "user-2", currentGeneration)
	assert.False(t, assignmentFound)
	assert.False(t, errorFound)
}

func TestLRUBlindExperimentCacheCapacityEvictionConcurrentGenerationReadDoesNotDeadlock(t *testing.T) {
	cache := auth.NewLRUBlindExperimentCache(1, time.Minute, time.Now)
	generation := cache.InstallationGeneration("inst-1")
	require.True(t, cache.SetAtGeneration("inst-1", "initial-user", auth.BlindExperimentState{Active: true, Arm: auth.BlindExperimentArmRouterOn}, generation))

	start := make(chan struct{})
	done := make(chan struct{}, 2)
	go func() {
		<-start
		for range 2_000 {
			cache.GetAtGeneration("inst-1", "initial-user", generation)
		}
		done <- struct{}{}
	}()
	go func() {
		<-start
		for index := range 2_000 {
			cache.SetAtGeneration("inst-1", "capacity-user-"+strconv.Itoa(index), auth.BlindExperimentState{Active: true, Arm: auth.BlindExperimentArmPassthrough}, generation)
		}
		done <- struct{}{}
	}()
	close(start)

	for range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent generation reads and forced capacity evictions deadlocked")
		}
	}
}

type fakeBlindExperimentRepository struct {
	record  auth.BlindExperimentRecord
	err     error
	calls   int
	started chan struct{}
	release chan struct{}
}

func (repository *fakeBlindExperimentRepository) GetForUser(context.Context, string, string) (auth.BlindExperimentRecord, error) {
	repository.calls++
	if repository.started != nil {
		close(repository.started)
	}
	if repository.release != nil {
		<-repository.release
	}
	if repository.err != nil {
		return auth.BlindExperimentRecord{}, repository.err
	}
	return repository.record, nil
}

func TestResolveAndStashUserBlindExperimentOverrideAndCache(t *testing.T) {
	users := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	experiments := &fakeBlindExperimentRepository{record: auth.BlindExperimentRecord{
		Configured:          true,
		Enabled:             true,
		RouterOnPercentage:  100,
		Seed:                "seed",
		CanonicalSubjectKey: "account-7",
		ManualOverride:      auth.BlindExperimentArmPassthrough,
	}}
	service := makeServiceWithUsers(t, users).
		WithBlindExperiments(experiments, auth.NewLRUBlindExperimentCache(10, time.Minute, time.Now))

	firstContext := service.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")
	secondContext := service.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")

	firstState, firstActive := auth.BlindExperimentFrom(firstContext)
	secondState, secondActive := auth.BlindExperimentFrom(secondContext)
	require.True(t, firstActive)
	require.True(t, secondActive)
	assert.Equal(t, auth.BlindExperimentArmPassthrough, firstState.Arm)
	assert.Equal(t, auth.BlindExperimentAssignmentManual, firstState.AssignmentSource)
	assert.Equal(t, "account-7", firstState.CanonicalSubjectKey)
	assert.Equal(t, firstState, secondState)
	assert.Equal(t, 1, experiments.calls)
}

func TestResolveAndStashUserBlindExperimentWithoutCache(t *testing.T) {
	users := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	experiments := &fakeBlindExperimentRepository{record: auth.BlindExperimentRecord{
		Configured:          true,
		Enabled:             true,
		RouterOnPercentage:  0,
		Seed:                "seed",
		CanonicalSubjectKey: "account-7",
	}}
	service := makeServiceWithUsers(t, users).WithBlindExperiments(experiments, nil)

	requestContext := service.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")

	state, active := auth.BlindExperimentFrom(requestContext)
	require.True(t, active)
	assert.Equal(t, auth.BlindExperimentArmPassthrough, state.Arm)
	assert.Equal(t, 1, experiments.calls)
}

func TestResolveAndStashUserBlindExperimentFetchFailureFailsOpen(t *testing.T) {
	users := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	experiments := &fakeBlindExperimentRepository{err: errors.New("database unavailable")}
	service := makeServiceWithUsers(t, users).
		WithBlindExperiments(experiments, auth.NewLRUBlindExperimentCache(10, 10*time.Millisecond, time.Now))

	requestContext := service.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")

	_, active := auth.BlindExperimentFrom(requestContext)
	assert.False(t, active)
	service.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")
	assert.Equal(t, 1, experiments.calls, "failed reads should be negatively cached during an outage")
	require.Eventually(t, func() bool {
		service.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")
		return experiments.calls == 2
	}, time.Second, 5*time.Millisecond, "failed reads should retry after the short outage cache window")
}

func TestResolveAndStashUserBlindExperimentRetriesUsingCacheClock(t *testing.T) {
	users := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	experiments := &fakeBlindExperimentRepository{err: errors.New("database unavailable")}
	current := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	cache := auth.NewLRUBlindExperimentCache(10, time.Minute, func() time.Time { return current })
	service := makeServiceWithUsers(t, users).WithBlindExperiments(experiments, cache)

	service.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")
	assert.Equal(t, 1, experiments.calls)

	current = current.Add(11 * time.Second)
	service.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")
	assert.Equal(t, 2, experiments.calls, "the injected cache clock must control the error retry boundary")
}

func TestResolveAndStashUserBlindExperimentDropsFetchInvalidatedDuringRead(t *testing.T) {
	users := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	experiments := &fakeBlindExperimentRepository{
		record: auth.BlindExperimentRecord{
			Configured:          true,
			Enabled:             true,
			RouterOnPercentage:  0,
			Seed:                "stale-seed",
			CanonicalSubjectKey: "account-7",
		},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	cache := auth.NewLRUBlindExperimentCache(10, time.Minute, time.Now)
	service := makeServiceWithUsers(t, users).WithBlindExperiments(experiments, cache)

	contextResult := make(chan context.Context, 1)
	go func() {
		contextResult <- service.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")
	}()
	<-experiments.started
	cache.InvalidateInstallation("inst-1")
	close(experiments.release)
	requestContext := <-contextResult

	_, active := auth.BlindExperimentFrom(requestContext)
	assert.False(t, active, "the request must fail open after its assignment was invalidated")
	_, found := cache.GetAtGeneration("inst-1", "user-42", cache.InstallationGeneration("inst-1"))
	assert.False(t, found, "a pre-invalidation repository result must not survive in the cache")
}

func TestResolveAndStashUserBlindExperimentKeepsFetchAcrossUnrelatedInvalidation(t *testing.T) {
	users := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	experiments := &fakeBlindExperimentRepository{
		record: auth.BlindExperimentRecord{
			Configured:          true,
			Enabled:             true,
			RouterOnPercentage:  100,
			Seed:                "seed",
			CanonicalSubjectKey: "account-7",
		},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	cache := auth.NewLRUBlindExperimentCache(10, time.Minute, time.Now)
	service := makeServiceWithUsers(t, users).WithBlindExperiments(experiments, cache)

	contextResult := make(chan context.Context, 1)
	go func() {
		contextResult <- service.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")
	}()
	<-experiments.started
	cache.InvalidateInstallation("inst-2")
	close(experiments.release)
	requestContext := <-contextResult

	state, active := auth.BlindExperimentFrom(requestContext)
	require.True(t, active, "another installation's invalidation must not discard this fetch")
	assert.Equal(t, auth.BlindExperimentArmRouterOn, state.Arm)
	cacheState, found := cache.GetAtGeneration("inst-1", "user-42", cache.InstallationGeneration("inst-1"))
	require.True(t, found)
	assert.Equal(t, state, cacheState)
}
