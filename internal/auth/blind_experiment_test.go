package auth_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
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

type fakeBlindExperimentRead struct {
	record auth.BlindExperimentRecord
	err    error
}

type fakeBlindExperimentRepository struct {
	record    auth.BlindExperimentRecord
	err       error
	reads     []fakeBlindExperimentRead
	calls     int
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
	onRead    func()

	installationEnabled bool
	installationErr     error
}

func (repository *fakeBlindExperimentRepository) GetForUser(context.Context, string, string) (auth.BlindExperimentRecord, error) {
	repository.calls++
	if repository.started != nil {
		repository.startOnce.Do(func() { close(repository.started) })
	}
	if repository.release != nil {
		<-repository.release
	}
	if repository.onRead != nil {
		repository.onRead()
	}
	if len(repository.reads) > 0 {
		read := repository.reads[min(repository.calls, len(repository.reads))-1]
		return read.record, read.err
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

func TestBlindExperimentCohortChangesAtBoundaryWithoutCacheRefresh(t *testing.T) {
	start := time.Date(2026, time.October, 5, 7, 0, 0, 0, time.UTC)
	now := start.Add(time.Hour)
	users := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	experiments := &fakeBlindExperimentRepository{record: auth.BlindExperimentRecord{
		Configured: true, Enabled: true, CanonicalSubjectKey: "account-7",
		CohortExperimentID: "experiment-1", CohortGroupID: 3, CohortRevision: 1,
		CohortStartsAt: start, CohortEndsAt: start.Add(14 * 24 * time.Hour),
		CohortSchedule: []auth.BlindExperimentPhase{
			{Index: 1, StartsAt: start, EndsAt: start.Add(7 * 24 * time.Hour), Arm: auth.BlindExperimentArmPassthrough},
			{Index: 2, StartsAt: start.Add(7 * 24 * time.Hour), EndsAt: start.Add(14 * 24 * time.Hour), Arm: auth.BlindExperimentArmRouterOn},
		},
	}}
	service := auth.NewService(&fakeInstallationRepository{}, &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{}},
		nil, users, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return now }).
		WithBlindExperiments(experiments, auth.NewLRUBlindExperimentCache(10, time.Hour, func() time.Time { return now }))

	first, active := auth.BlindExperimentFrom(service.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", ""))
	require.True(t, active)
	assert.Equal(t, auth.BlindExperimentArmPassthrough, first.Arm)
	assert.Equal(t, 1, first.CohortPhaseIndex)

	now = start.Add(7 * 24 * time.Hour)
	second, active := auth.BlindExperimentFrom(service.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", ""))
	require.True(t, active)
	assert.Equal(t, auth.BlindExperimentArmRouterOn, second.Arm)
	assert.Equal(t, 2, second.CohortPhaseIndex)
	assert.Equal(t, 3, second.CohortGroupID)
	assert.Equal(t, 1, experiments.calls)

	now = start.Add(14 * 24 * time.Hour)
	_, active = auth.BlindExperimentFrom(service.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", ""))
	assert.False(t, active)
}

func TestBlindExperimentCohortManualArmDoesNotChangeGroup(t *testing.T) {
	start := time.Date(2026, time.October, 5, 0, 0, 0, 0, time.UTC)
	state := auth.BlindExperimentState{
		Active: true, Enabled: true, CohortExperimentID: "experiment-1", CohortGroupID: 3,
		CohortStartsAt: start, CohortEndsAt: start.Add(24 * time.Hour),
		ManualOverride: auth.BlindExperimentArmRouterOn,
		CohortSchedule: []auth.BlindExperimentPhase{{Index: 1, StartsAt: start, EndsAt: start.Add(24 * time.Hour), Arm: auth.BlindExperimentArmPassthrough}},
	}.AtTime(start)
	assert.True(t, state.Active)
	assert.Equal(t, auth.BlindExperimentArmPassthrough, state.ScheduledArm)
	assert.Equal(t, auth.BlindExperimentArmRouterOn, state.Arm)
	assert.Equal(t, auth.BlindExperimentAssignmentManual, state.AssignmentSource)
	assert.Equal(t, 3, state.CohortGroupID)
}

func TestBlindExperimentEmergencyOverrideChangesAtExactBoundaries(t *testing.T) {
	start := time.Date(2026, time.October, 5, 0, 0, 0, 0, time.UTC)
	state := auth.BlindExperimentState{Enabled: true, CohortExperimentID: "experiment-1", CohortGroupID: 3,
		CohortStartsAt: start, CohortEndsAt: start.Add(24 * time.Hour),
		CohortSchedule:  []auth.BlindExperimentPhase{{Index: 1, StartsAt: start, EndsAt: start.Add(24 * time.Hour), Arm: auth.BlindExperimentArmRouterOn}},
		CohortOverrides: []auth.BlindExperimentEmergencyOverride{{StartsAt: start.Add(time.Hour), EndsAt: start.Add(2 * time.Hour), Arm: auth.BlindExperimentArmPassthrough}},
	}
	before := state.AtTime(start.Add(time.Hour - time.Nanosecond))
	assert.Equal(t, auth.BlindExperimentArmRouterOn, before.Arm)
	atStart := state.AtTime(start.Add(time.Hour))
	assert.Equal(t, auth.BlindExperimentArmPassthrough, atStart.Arm)
	assert.Equal(t, auth.BlindExperimentArmRouterOn, atStart.ScheduledArm)
	assert.Equal(t, auth.BlindExperimentAssignmentManual, atStart.AssignmentSource)
	assert.Equal(t, 3, atStart.CohortGroupID)
	after := state.AtTime(start.Add(2 * time.Hour))
	assert.Equal(t, auth.BlindExperimentArmRouterOn, after.Arm)
	assert.Equal(t, auth.BlindExperimentAssignmentAutomatic, after.AssignmentSource)
}

func TestBlindExperimentCohortDstBoundaryUsesAbsoluteInstants(t *testing.T) {
	location, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)
	first := time.Date(2026, time.October, 26, 0, 0, 0, 0, location).UTC()
	second := time.Date(2026, time.November, 2, 0, 0, 0, 0, location).UTC()
	end := time.Date(2026, time.November, 9, 0, 0, 0, 0, location).UTC()
	assert.Equal(t, time.Hour*169, second.Sub(first))
	state := auth.BlindExperimentState{Enabled: true, CohortExperimentID: "experiment-1", CohortGroupID: 1,
		CohortStartsAt: first, CohortEndsAt: end, CohortSchedule: []auth.BlindExperimentPhase{
			{Index: 1, StartsAt: first, EndsAt: second, Arm: auth.BlindExperimentArmRouterOn},
			{Index: 2, StartsAt: second, EndsAt: end, Arm: auth.BlindExperimentArmPassthrough},
		}}
	assert.Equal(t, auth.BlindExperimentArmRouterOn, state.AtTime(second.Add(-time.Nanosecond)).Arm)
	assert.Equal(t, auth.BlindExperimentArmPassthrough, state.AtTime(second).Arm)
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

func TestResolveAndStashUserBlindExperimentRereadsFetchInvalidatedDuringRead(t *testing.T) {
	users := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	experiments := &fakeBlindExperimentRepository{
		record: auth.BlindExperimentRecord{
			Configured:          true,
			Enabled:             true,
			RouterOnPercentage:  0,
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
	staleGeneration := cache.InstallationGeneration("inst-1")
	cache.InvalidateInstallation("inst-1")
	close(experiments.release)
	requestContext := <-contextResult

	state, active := auth.BlindExperimentFrom(requestContext)
	require.True(t, active, "an invalidation during the read must not drop the request out of the experiment")
	assert.Equal(t, auth.BlindExperimentArmPassthrough, state.Arm)
	assert.Equal(t, 2, experiments.calls, "an invalidated read must be repeated at the new generation")
	_, staleFound := cache.GetAtGeneration("inst-1", "user-42", staleGeneration)
	assert.False(t, staleFound, "a pre-invalidation repository result must not survive in the cache")
	cached, found := cache.GetAtGeneration("inst-1", "user-42", cache.InstallationGeneration("inst-1"))
	require.True(t, found, "the re-read result should be cached at the current generation")
	assert.Equal(t, auth.BlindExperimentArmPassthrough, cached.Arm)
}

func TestResolveAndStashUserBlindExperimentKeepsLastReadWhenInvalidationsPersist(t *testing.T) {
	users := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	cache := auth.NewLRUBlindExperimentCache(10, time.Minute, time.Now)
	routerOn := auth.BlindExperimentRecord{Configured: true, Enabled: true, RouterOnPercentage: 100, Seed: "seed", CanonicalSubjectKey: "account-7"}
	passthrough := auth.BlindExperimentRecord{Configured: true, Enabled: true, RouterOnPercentage: 0, Seed: "seed", CanonicalSubjectKey: "account-7"}
	experiments := &fakeBlindExperimentRepository{
		reads:  []fakeBlindExperimentRead{{record: routerOn}, {record: routerOn}, {record: passthrough}},
		onRead: func() { cache.InvalidateInstallation("inst-1") },
	}
	service := makeServiceWithUsers(t, users).WithBlindExperiments(experiments, cache)

	requestContext := service.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")

	state, active := auth.BlindExperimentFrom(requestContext)
	require.True(t, active, "persistent invalidations must not silently route a passthrough experiment")
	assert.Equal(t, auth.BlindExperimentArmPassthrough, state.Arm, "the final read, not an earlier one, must supply the arm")
	assert.Equal(t, len(experiments.reads), experiments.calls, "the read should be retried before falling back to the last result")
	_, found := cache.GetAtGeneration("inst-1", "user-42", cache.InstallationGeneration("inst-1"))
	assert.False(t, found, "a result invalidated during its read must not be cached")
}

func TestResolveAndStashUserBlindExperimentKeepsInvalidatedReadWhenRetryFails(t *testing.T) {
	users := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	cache := auth.NewLRUBlindExperimentCache(10, time.Minute, time.Now)
	experiments := &fakeBlindExperimentRepository{
		reads: []fakeBlindExperimentRead{
			{record: auth.BlindExperimentRecord{Configured: true, Enabled: true, RouterOnPercentage: 0, Seed: "seed", CanonicalSubjectKey: "account-7"}},
			{err: errors.New("database unavailable")},
		},
		onRead: func() { cache.InvalidateInstallation("inst-1") },
	}
	service := makeServiceWithUsers(t, users).WithBlindExperiments(experiments, cache)

	requestContext := service.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")

	state, active := auth.BlindExperimentFrom(requestContext)
	require.True(t, active, "a failed retry must not discard the assignment already read for this request")
	assert.Equal(t, auth.BlindExperimentArmPassthrough, state.Arm)
	assert.Equal(t, 2, experiments.calls)
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

func sessionModeService(t *testing.T, record auth.BlindExperimentRecord) *auth.Service {
	t.Helper()
	users := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	return makeServiceWithUsers(t, users).WithBlindExperiments(&fakeBlindExperimentRepository{record: record}, nil)
}

func sessionState(t *testing.T, service *auth.Service, sessionKey string) auth.BlindExperimentState {
	t.Helper()
	ctx := service.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")
	state, active := auth.BlindExperimentFrom(service.WithBlindExperimentSession(ctx, "inst-1", sessionKey))
	require.True(t, active)
	return state
}

func TestBlindExperimentSessionModeSplitsOneUsersSessions(t *testing.T) {
	service := sessionModeService(t, auth.BlindExperimentRecord{
		Configured: true, Enabled: true, RouterOnPercentage: 50, Seed: "seed",
		CanonicalSubjectKey: "account-7", SessionMode: true,
	})
	arms := map[auth.BlindExperimentArm]int{}
	for index := range 200 {
		key := auth.BlindExperimentSessionSubjectKey("key-1", "session-"+strconv.Itoa(index))
		state := sessionState(t, service, key)
		assert.Equal(t, key, state.SessionSubjectKey)
		assert.Equal(t, auth.BlindExperimentModeSession, state.Mode)
		assert.Equal(t, auth.BlindExperimentAssignmentAutomatic, state.AssignmentSource)
		assert.Equal(t, state.Arm, sessionState(t, service, key).Arm)
		arms[state.Arm]++
	}
	assert.Greater(t, arms[auth.BlindExperimentArmRouterOn], 60)
	assert.Greater(t, arms[auth.BlindExperimentArmPassthrough], 60)
}

func TestBlindExperimentSessionKeyIsScopedToCredential(t *testing.T) {
	assert.Empty(t, auth.BlindExperimentSessionSubjectKey("key-1", ""))
	assert.Empty(t, auth.BlindExperimentSessionSubjectKey("", "session-1"))
	assert.Equal(t, auth.BlindExperimentSessionSubjectKey("key-1", "session-1"), auth.BlindExperimentSessionSubjectKey("key-1", "session-1"))
	assert.NotEqual(t, auth.BlindExperimentSessionSubjectKey("key-1", "session-1"), auth.BlindExperimentSessionSubjectKey("key-2", "session-1"))
}

func TestBlindExperimentSessionModeWithoutSessionFallsBackToUserDraw(t *testing.T) {
	record := auth.BlindExperimentRecord{
		Configured: true, Enabled: true, RouterOnPercentage: 50, Seed: "seed",
		CanonicalSubjectKey: "account-7", SessionMode: true,
	}
	state := sessionState(t, sessionModeService(t, record), "")
	assert.Equal(t, auth.BlindExperimentArmPassthrough, state.Arm)
	assert.Empty(t, state.SessionSubjectKey)
	assert.Equal(t, auth.BlindExperimentModeSession, state.Mode)
}

func TestBlindExperimentSessionModeIgnoresManualOverride(t *testing.T) {
	service := sessionModeService(t, auth.BlindExperimentRecord{
		Configured: true, Enabled: true, RouterOnPercentage: 100, Seed: "seed",
		CanonicalSubjectKey: "account-7", ManualOverride: auth.BlindExperimentArmPassthrough,
		AutomaticArm: auth.BlindExperimentArmPassthrough, SessionMode: true,
	})
	for _, key := range []string{"", auth.BlindExperimentSessionSubjectKey("key-1", "session-1")} {
		state := sessionState(t, service, key)
		assert.Equal(t, auth.BlindExperimentArmRouterOn, state.Arm)
		assert.Equal(t, auth.BlindExperimentAssignmentAutomatic, state.AssignmentSource)
	}
}

func TestBlindExperimentSessionModeDisabledIsInactive(t *testing.T) {
	service := sessionModeService(t, auth.BlindExperimentRecord{
		Configured: true, Enabled: false, RouterOnPercentage: 50, Seed: "seed",
		CanonicalSubjectKey: "account-7", SessionMode: true,
	})
	ctx := service.ResolveAndStashUser(context.Background(), "inst-1", "alice@example.com", "", "")
	_, active := auth.BlindExperimentFrom(service.WithBlindExperimentSession(ctx, "inst-1", auth.BlindExperimentSessionSubjectKey("key-1", "session-1")))
	assert.False(t, active)
}

func TestBlindExperimentUserModeIgnoresSession(t *testing.T) {
	service := sessionModeService(t, auth.BlindExperimentRecord{
		Configured: true, Enabled: true, RouterOnPercentage: 0, Seed: "seed",
		CanonicalSubjectKey: "account-7", AutomaticArm: auth.BlindExperimentArmRouterOn,
	})
	state := sessionState(t, service, auth.BlindExperimentSessionSubjectKey("key-1", "session-1"))
	assert.Equal(t, auth.BlindExperimentArmRouterOn, state.Arm)
	assert.Equal(t, auth.BlindExperimentModeUser, state.Mode)
	assert.Empty(t, state.SessionSubjectKey)
}

func TestBlindExperimentCohortModeIgnoresSession(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	users := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	service := auth.NewService(&fakeInstallationRepository{}, &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{}},
		nil, users, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return now }).WithBlindExperiments(&fakeBlindExperimentRepository{record: auth.BlindExperimentRecord{
		Configured: true, Enabled: true, RouterOnPercentage: 0, Seed: "seed", CanonicalSubjectKey: "account-7",
		SessionMode:        true,
		CohortExperimentID: "cohort-1", CohortGroupID: 1, CohortRevision: 1,
		CohortStartsAt: now.Add(-time.Hour), CohortEndsAt: now.Add(time.Hour),
		CohortSchedule: []auth.BlindExperimentPhase{{Index: 0, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), Arm: auth.BlindExperimentArmRouterOn}},
	}}, nil)
	state := sessionState(t, service, auth.BlindExperimentSessionSubjectKey("key-1", "session-1"))
	assert.Equal(t, auth.BlindExperimentArmRouterOn, state.Arm)
	assert.Equal(t, auth.BlindExperimentModeCohort, state.Mode)
	assert.Empty(t, state.SessionSubjectKey)
}

type fakeBlindExperimentSessionRepo struct {
	record   auth.BlindExperimentRecord
	arms     map[string]auth.BlindExperimentArm
	claimErr error
	reads    int
}

func (repo *fakeBlindExperimentSessionRepo) GetForInstallation(context.Context, string) (auth.BlindExperimentRecord, error) {
	repo.reads++
	return repo.record, nil
}

func (repo *fakeBlindExperimentSessionRepo) ClaimSessionArm(_ context.Context, _ string, sessionSubjectKey string, arm auth.BlindExperimentArm) (auth.BlindExperimentArm, error) {
	if repo.claimErr != nil {
		return "", repo.claimErr
	}
	if existing, found := repo.arms[sessionSubjectKey]; found {
		return existing, nil
	}
	repo.arms[sessionSubjectKey] = arm
	return arm, nil
}

func TestBlindExperimentSessionModeKeepsFirstArmAfterConfigChange(t *testing.T) {
	record := auth.BlindExperimentRecord{
		Configured: true, Enabled: true, RouterOnPercentage: 100, Seed: "seed",
		CanonicalSubjectKey: "account-7", SessionMode: true,
	}
	sessions := &fakeBlindExperimentSessionRepo{arms: map[string]auth.BlindExperimentArm{}}
	runningSessionKey := auth.BlindExperimentSessionSubjectKey("key-1", "session-1")
	assert.Equal(t, auth.BlindExperimentArmRouterOn, sessionState(t, sessionModeService(t, record).WithBlindExperimentSessions(sessions), runningSessionKey).Arm)

	record.RouterOnPercentage = 0
	changed := sessionModeService(t, record).WithBlindExperimentSessions(sessions)
	assert.Equal(t, auth.BlindExperimentArmRouterOn, sessionState(t, changed, runningSessionKey).Arm)
	assert.Equal(t, auth.BlindExperimentArmPassthrough, sessionState(t, changed, auth.BlindExperimentSessionSubjectKey("key-1", "session-2")).Arm)
}

func TestBlindExperimentSessionModeAssignsUnattributedSessions(t *testing.T) {
	sessions := &fakeBlindExperimentSessionRepo{
		record: auth.BlindExperimentRecord{Configured: true, Enabled: true, RouterOnPercentage: 100, Seed: "seed", SessionMode: true},
		arms:   map[string]auth.BlindExperimentArm{},
	}
	service := makeServiceWithUsers(t, &fakeUserRepo{}).
		WithBlindExperiments(&fakeBlindExperimentRepository{}, nil).
		WithBlindExperimentSessions(sessions)
	sessionKey := auth.BlindExperimentSessionSubjectKey("key-1", "session-1")

	state, active := auth.BlindExperimentFrom(service.WithBlindExperimentSession(context.Background(), "inst-1", sessionKey))
	require.True(t, active)
	assert.Equal(t, auth.BlindExperimentArmRouterOn, state.Arm)
	assert.Equal(t, auth.BlindExperimentModeSession, state.Mode)
	assert.Equal(t, sessionKey, state.SessionSubjectKey)
	assert.Equal(t, auth.BlindExperimentArmRouterOn, sessions.arms[sessionKey])

	_, active = auth.BlindExperimentFrom(service.WithBlindExperimentSession(context.Background(), "inst-1", ""))
	assert.False(t, active)
}

func TestBlindExperimentUserModeLeavesUnattributedSessionsUnassigned(t *testing.T) {
	sessions := &fakeBlindExperimentSessionRepo{
		record: auth.BlindExperimentRecord{Configured: true, Enabled: true, RouterOnPercentage: 100, Seed: "seed"},
		arms:   map[string]auth.BlindExperimentArm{},
	}
	service := makeServiceWithUsers(t, &fakeUserRepo{}).WithBlindExperimentSessions(sessions)
	_, active := auth.BlindExperimentFrom(service.WithBlindExperimentSession(context.Background(), "inst-1", auth.BlindExperimentSessionSubjectKey("key-1", "session-1")))
	assert.False(t, active)
	assert.Empty(t, sessions.arms)
}

func TestBlindExperimentSessionModeServesDrawWhenClaimFails(t *testing.T) {
	record := auth.BlindExperimentRecord{
		Configured: true, Enabled: true, RouterOnPercentage: 100, Seed: "seed",
		CanonicalSubjectKey: "account-7", SessionMode: true,
	}
	sessions := &fakeBlindExperimentSessionRepo{arms: map[string]auth.BlindExperimentArm{}, claimErr: errors.New("database unavailable")}
	service := sessionModeService(t, record).WithBlindExperimentSessions(sessions)
	sessionKey := auth.BlindExperimentSessionSubjectKey("key-1", "session-1")
	assert.Equal(t, auth.BlindExperimentArmRouterOn, sessionState(t, service, sessionKey).Arm)

	sessions.claimErr = nil
	sessions.arms[sessionKey] = auth.BlindExperimentArmPassthrough
	assert.Equal(t, auth.BlindExperimentArmPassthrough, sessionState(t, service, sessionKey).Arm)
}

func TestBlindExperimentSessionModeCachesInstallationConfig(t *testing.T) {
	sessions := &fakeBlindExperimentSessionRepo{
		record: auth.BlindExperimentRecord{Configured: true, Enabled: true, RouterOnPercentage: 100, Seed: "seed", SessionMode: true},
		arms:   map[string]auth.BlindExperimentArm{},
	}
	service := makeServiceWithUsers(t, &fakeUserRepo{}).
		WithBlindExperiments(&fakeBlindExperimentRepository{}, auth.NewLRUBlindExperimentCache(16, time.Minute, time.Now)).
		WithBlindExperimentSessions(sessions)
	for _, sessionID := range []string{"session-1", "session-2", "session-3"} {
		_, active := auth.BlindExperimentFrom(service.WithBlindExperimentSession(context.Background(), "inst-1", auth.BlindExperimentSessionSubjectKey("key-1", sessionID)))
		require.True(t, active)
	}
	assert.Equal(t, 1, sessions.reads)
}

func (repository *fakeBlindExperimentRepository) InstallationExperimentEnabled(context.Context, string) (bool, error) {
	return repository.installationEnabled, repository.installationErr
}

func TestInstallationBlindExperimentEnabled(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		experiments *fakeBlindExperimentRepository
		want        bool
	}{
		{name: "no experiment repository"},
		{name: "no enabled experiment", experiments: &fakeBlindExperimentRepository{}},
		{name: "enabled experiment", experiments: &fakeBlindExperimentRepository{installationEnabled: true}, want: true},
		{name: "lookup error fails closed", experiments: &fakeBlindExperimentRepository{installationErr: errors.New("db down")}, want: true},
	} {
		service := makeServiceWithUsers(t, nil)
		if tc.experiments != nil {
			service = service.WithBlindExperiments(tc.experiments, nil)
		}
		assert.Equal(t, tc.want, service.InstallationBlindExperimentEnabled(ctx, "inst-1"), tc.name)
	}
}
