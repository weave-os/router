package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
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
