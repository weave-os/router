package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/health"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

type startupRouterFunc func(context.Context, router.Request) (router.Decision, error)

func (f startupRouterFunc) Route(ctx context.Context, r router.Request) (router.Decision, error) {
	return f(ctx, r)
}

type startupPreviewFunc func(context.Context, router.Request) (policy.PreviewResult, error)

func (f startupPreviewFunc) PreviewRoute(ctx context.Context, r router.Request) (policy.PreviewResult, error) {
	return f(ctx, r)
}

func TestStartupExercisesLocalScoringAndRejectsInvalidDecision(t *testing.T) {
	cases := []struct {
		name     string
		decision router.Decision
		failure  error
	}{
		{name: "missing decision"},
		{name: "inference failure", failure: errors.New("model unavailable")},
		{name: "nonfinite score", decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "synthetic-model", Metadata: &router.RoutingMetadata{ChosenScore: float32(math.NaN())}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := warmLocalRouter(context.Background(), startupRouterFunc(func(ctx context.Context, r router.Request) (router.Decision, error) {
				require.NotEmpty(t, r.PromptText)
				return test.decision, test.failure
			}))
			require.Error(t, err)
		})
	}
}

func TestStartupClassifierRequiresCompleteFiniteProbabilities(t *testing.T) {
	for _, probabilities := range []map[string]float64{nil, {"synthetic": math.NaN()}, {"synthetic": 1.1}, {"another": 1}} {
		err := warmClassifier(context.Background(), startupPreviewFunc(func(context.Context, router.Request) (policy.PreviewResult, error) {
			return policy.PreviewResult{ClassOrder: []string{"synthetic"}, ClassProbabilities: probabilities}, nil
		}))
		require.Error(t, err)
	}
	require.NoError(t, warmClassifier(context.Background(), startupPreviewFunc(func(context.Context, router.Request) (policy.PreviewResult, error) {
		return policy.PreviewResult{ClassOrder: []string{"synthetic"}, ClassProbabilities: map[string]float64{"synthetic": 1}}, nil
	})))
}

func TestStartupDatabaseRetriesButKeepsTotalDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err := warmStartupDatabase(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context) error { return errors.New("database unavailable") })
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestEssentialTaskDeathRequiresRestartButCancellationDoesNot(t *testing.T) {
	require.ErrorContains(t, runEssentialTask(context.Background(), func() { panic("terminal failure") }), "panicked")
	require.ErrorContains(t, runEssentialTask(context.Background(), func() {}), "stopped unexpectedly")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, runEssentialTask(ctx, func() {}))
}

func TestStartupCompletionWaitsForFinalReadinessAndProbesStayLatched(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var ready atomic.Bool
		capacity, err := health.NewCapacity(health.Limits{})
		require.NoError(t, err)
		checked := make(chan struct{})
		release := make(chan struct{})
		calls := 0
		checker := newReadinessChecker(databasePingerFunc(func(context.Context) error {
			calls++
			close(checked)
			<-release
			return nil
		}), nil, strategySet{router.StrategyCluster: true}, router.StrategyCluster)
		engine := gin.New()
		engine.GET("/startupz", admin.StartupHandler(ready.Load))
		probe := func() int {
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/startupz", nil))
			return response.Code
		}
		completed := make(chan error, 1)
		go func() {
			completed <- completeStartup(context.Background(), checker, capacity, nil, &ready)
		}()
		<-checked
		require.Equal(t, http.StatusServiceUnavailable, probe())
		close(release)
		require.NoError(t, <-completed)
		require.Equal(t, http.StatusOK, probe())
		require.Equal(t, http.StatusOK, probe())
		require.Equal(t, 1, calls)
	})
}

func TestStartupCompletionRejectsUnhealthyInstance(t *testing.T) {
	for _, failure := range []string{"database", "classifier", "strategy", "capacity", "essential task", "canceled boot"} {
		t.Run(failure, func(t *testing.T) {
			var ready atomic.Bool
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			capacity, err := health.NewCapacity(health.Limits{MaxRequests: 1})
			require.NoError(t, err)
			checker := newReadinessChecker(databasePingerFunc(healthyDatabase), nil, strategySet{router.StrategyCluster: true}, router.StrategyCluster)
			taskErrors := make(chan error, 1)
			switch failure {
			case "database":
				checker.database = databasePingerFunc(func(context.Context) error { return errors.New("database lost during warmup") })
			case "classifier":
				checker.hmm = healthCheckerFunc(func(context.Context) error { return errors.New("classifier unavailable") })
			case "strategy":
				checker.strategies = strategySet{}
			case "capacity":
				permit := capacity.TryAcquire()
				defer permit.Release()
			case "essential task":
				taskErrors <- errors.New("policy manager stopped")
			case "canceled boot":
				cancel()
			}
			require.Error(t, completeStartup(ctx, checker, capacity, taskErrors, &ready))
			require.False(t, ready.Load())
		})
	}
}

func TestFinalStartupReadinessRespectsProbeAndRemainingBootBudgets(t *testing.T) {
	for _, remaining := range []time.Duration{time.Second, startupTimeout} {
		t.Run(remaining.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var ready atomic.Bool
				capacity, err := health.NewCapacity(health.Limits{})
				require.NoError(t, err)
				checker := newReadinessChecker(databasePingerFunc(func(ctx context.Context) error {
					<-ctx.Done()
					return ctx.Err()
				}), nil, strategySet{router.StrategyCluster: true}, router.StrategyCluster)
				ctx, cancel := context.WithTimeout(context.Background(), remaining)
				defer cancel()
				started := time.Now()
				require.ErrorIs(t, completeStartup(ctx, checker, capacity, nil, &ready), context.DeadlineExceeded)
				require.Equal(t, min(remaining, 3*time.Second), time.Since(started))
				require.False(t, ready.Load())
			})
		})
	}
}
