package requestcontext_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/requestcontext"
)

func TestPreparationBoundsDatabaseAndSkipsDoomedReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		health := requestcontext.NewDependencyHealth()
		ctx, state, owner := requestcontext.BeginPreparation(context.Background(), health, requestcontext.DefaultPreparationLimits())
		defer state.Close()
		assert.True(t, owner)
		started := time.Now()
		read, finish, err := state.Start(ctx, requestcontext.DependencyDatabase)
		require.NoError(t, err)
		<-read.Done()
		finish(read.Err())
		assert.Equal(t, 250*time.Millisecond, time.Since(started))
		assert.Equal(t, requestcontext.ReasonDatabaseUnavailable, state.Failure().Reason())
		_, _, err = state.Start(ctx, requestcontext.DependencyPolicy)
		assert.ErrorIs(t, err, requestcontext.ErrDependencyUnavailable)

		secondCtx, second, _ := requestcontext.BeginPreparation(context.Background(), health, requestcontext.DefaultPreparationLimits())
		defer second.Close()
		_, _, err = second.Start(secondCtx, requestcontext.DependencyDatabase)
		assert.ErrorIs(t, err, requestcontext.ErrDependencyUnavailable)
		assert.Equal(t, 250*time.Millisecond, time.Since(started), "cooldown rejection must not consume another DB timeout")
	})
}

func TestPreparationAllowsOneRecoveryProbe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		health := requestcontext.NewDependencyHealth()
		limits := requestcontext.DefaultPreparationLimits()
		ctx, first, _ := requestcontext.BeginPreparation(context.Background(), health, limits)
		defer first.Close()
		_, finish, err := first.Start(ctx, requestcontext.DependencyDatabase)
		require.NoError(t, err)
		finish(errors.New("connection refused"))
		time.Sleep(5 * time.Second)

		probeCtx, probe, _ := requestcontext.BeginPreparation(context.Background(), health, limits)
		defer probe.Close()
		_, finishProbe, err := probe.Start(probeCtx, requestcontext.DependencyDatabase)
		require.NoError(t, err)
		otherCtx, other, _ := requestcontext.BeginPreparation(context.Background(), health, limits)
		defer other.Close()
		_, _, err = other.Start(otherCtx, requestcontext.DependencyDatabase)
		require.ErrorIs(t, err, requestcontext.ErrDependencyUnavailable)
		finishProbe(nil)

		recoveredCtx, recovered, _ := requestcontext.BeginPreparation(context.Background(), health, limits)
		defer recovered.Close()
		read, finishRecovered, err := recovered.Start(recoveredCtx, requestcontext.DependencyDatabase)
		require.NoError(t, err)
		assert.NoError(t, read.Err())
		finishRecovered(nil)
		assert.Nil(t, recovered.Failure())
	})
}

func TestProviderContextRestoresOnlyClientDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		type markerKey struct{}
		live, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ctx, state, _ := requestcontext.BeginPreparation(live, requestcontext.NewDependencyHealth(), requestcontext.DefaultPreparationLimits())
		defer state.Close()
		ctx = context.WithValue(ctx, markerKey{}, "resolved request value")
		_, nested, owner := requestcontext.BeginPreparation(ctx, requestcontext.NewDependencyHealth(), requestcontext.DefaultPreparationLimits())
		assert.False(t, owner)
		assert.Same(t, state, nested)
		time.Sleep(12 * time.Second)
		synctest.Wait()
		assert.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
		upstream := requestcontext.ProviderContext(ctx)
		assert.NoError(t, upstream.Err())
		assert.Equal(t, "resolved request value", upstream.Value(markerKey{}))
		assert.True(t, state.CanRelay())
		cancel()
		assert.ErrorIs(t, upstream.Err(), context.Canceled)
		assert.False(t, state.CanRelay())
	})
}

func TestPreparationNeverReplaysAStartedProvider(t *testing.T) {
	ctx, state, _ := requestcontext.BeginPreparation(context.Background(), requestcontext.NewDependencyHealth(), requestcontext.DefaultPreparationLimits())
	defer state.Close()
	state.ProviderStarted()
	state.Fail(requestcontext.DependencyDatabase, errors.New("post-response failure"))
	assert.False(t, state.CanRelay())
	assert.ErrorIs(t, state.Failure(), requestcontext.ErrDependencyUnavailable)
	assert.NoError(t, requestcontext.ProviderContext(ctx).Err())
}

func TestLateHealthyCallCannotReleaseRecoveryProbe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		health := requestcontext.NewDependencyHealth()
		limits := requestcontext.DefaultPreparationLimits()
		ctx, old, _ := requestcontext.BeginPreparation(context.Background(), health, limits)
		defer old.Close()
		_, finishOld, err := old.Start(ctx, requestcontext.DependencyPolicy)
		require.NoError(t, err)
		failingCtx, failing, _ := requestcontext.BeginPreparation(context.Background(), health, limits)
		defer failing.Close()
		_, finishFailure, err := failing.Start(failingCtx, requestcontext.DependencyPolicy)
		require.NoError(t, err)
		finishFailure(errors.New("policy unavailable"))
		time.Sleep(5 * time.Second)
		probeCtx, probe, _ := requestcontext.BeginPreparation(context.Background(), health, limits)
		defer probe.Close()
		_, finishProbe, err := probe.Start(probeCtx, requestcontext.DependencyPolicy)
		require.NoError(t, err)
		finishOld(nil)
		otherCtx, other, _ := requestcontext.BeginPreparation(context.Background(), health, limits)
		defer other.Close()
		_, _, err = other.Start(otherCtx, requestcontext.DependencyPolicy)
		assert.ErrorIs(t, err, requestcontext.ErrDependencyUnavailable)
		finishProbe(nil)
	})
}
