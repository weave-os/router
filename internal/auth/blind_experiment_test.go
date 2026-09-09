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

func TestAssignBlindExperimentArmPercentageOnlyMovesBoundarySubjects(t *testing.T) {
	for index := range 500 {
		subject := "subject-" + strconv.Itoa(index)
		atTwentyFive := auth.AssignBlindExperimentArm("seed", subject, 25)
		atSeventyFive := auth.AssignBlindExperimentArm("seed", subject, 75)
		if atTwentyFive == auth.BlindExperimentArmRouterOn {
			assert.Equal(t, auth.BlindExperimentArmRouterOn, atSeventyFive)
		}
	}
}

func TestLRUBlindExperimentCacheStoresInactiveAndInvalidatesByInstallation(t *testing.T) {
	cache := auth.NewLRUBlindExperimentCache(10, time.Minute)
	cache.Set("inst-1", "user-1", auth.BlindExperimentState{})
	cache.Set("inst-2", "user-2", auth.BlindExperimentState{Active: true, Arm: auth.BlindExperimentArmRouterOn})

	_, inactiveHit := cache.Get("user-1")
	require.True(t, inactiveHit, "a missing or disabled configuration must be negatively cached")

	cache.InvalidateInstallation("inst-1")
	_, firstFound := cache.Get("user-1")
	_, secondFound := cache.Get("user-2")
	assert.False(t, firstFound)
	assert.True(t, secondFound)
}

func TestLRUBlindExperimentCacheReassignsUserBetweenInstallations(t *testing.T) {
	cache := auth.NewLRUBlindExperimentCache(10, time.Minute)
	cache.Set("inst-1", "user-1", auth.BlindExperimentState{Active: true, Arm: auth.BlindExperimentArmRouterOn})
	cache.Set("inst-2", "user-1", auth.BlindExperimentState{Active: true, Arm: auth.BlindExperimentArmPassthrough})

	cache.InvalidateInstallation("inst-1")
	state, found := cache.Get("user-1")
	assert.True(t, found, "invalidating the previous installation must not evict the reassigned user")
	assert.Equal(t, auth.BlindExperimentArmPassthrough, state.Arm)

	cache.InvalidateInstallation("inst-2")
	_, found = cache.Get("user-1")
	assert.False(t, found)
}

func TestLRUBlindExperimentCacheTTLExpires(t *testing.T) {
	cache := auth.NewLRUBlindExperimentCache(10, 10*time.Millisecond)
	cache.Set("inst-1", "user-1", auth.BlindExperimentState{Active: true, Arm: auth.BlindExperimentArmPassthrough})

	require.Eventually(t, func() bool {
		_, found := cache.Get("user-1")
		return !found
	}, time.Second, 5*time.Millisecond)
}

type fakeBlindExperimentRepository struct {
	record auth.BlindExperimentRecord
	err    error
	calls  int
}

func (repository *fakeBlindExperimentRepository) GetForUser(context.Context, string, string) (auth.BlindExperimentRecord, error) {
	repository.calls++
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
		WithBlindExperiments(experiments, auth.NewLRUBlindExperimentCache(10, time.Minute))

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

func TestResolveAndStashUserBlindExperimentFetchFailureFailsOpen(t *testing.T) {
	users := &fakeUserRepo{user: &auth.User{ID: "user-42", InstallationID: "inst-1", Email: "alice@example.com"}}
	experiments := &fakeBlindExperimentRepository{err: errors.New("database unavailable")}
	service := makeServiceWithUsers(t, users).
		WithBlindExperiments(experiments, auth.NewLRUBlindExperimentCache(10, 10*time.Millisecond))

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
