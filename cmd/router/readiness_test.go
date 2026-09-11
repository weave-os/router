package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/router"
)

type databasePingerFunc func(context.Context) error

func (f databasePingerFunc) Ping(ctx context.Context) error {
	return f(ctx)
}

type healthCheckerFunc func(context.Context) error

func (f healthCheckerFunc) CheckHealth(ctx context.Context) error {
	return f(ctx)
}

type strategySet map[router.Strategy]bool

func (s strategySet) PolicyStrategyAvailable(strategy router.Strategy) bool {
	return s[strategy]
}

func healthyDatabase(context.Context) error { return nil }

func TestReadinessCheckerRequiresDatabase(t *testing.T) {
	databaseErr := errors.New("connection reset by peer")
	checker := newReadinessChecker(databasePingerFunc(func(context.Context) error {
		return databaseErr
	}), healthCheckerFunc(func(context.Context) error {
		t.Fatal("HMM readiness should not run when PostgreSQL is unavailable")
		return nil
	}), strategySet{router.StrategyCluster: true}, router.StrategyCluster)

	err := checker.CheckHealth(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, databaseErr)
	assert.ErrorContains(t, err, "postgres readiness check failed")
}

func TestReadinessCheckerChecksHMMAfterDatabase(t *testing.T) {
	var checks []string
	hmmErr := errors.New("sidecar unavailable")
	checker := newReadinessChecker(databasePingerFunc(func(context.Context) error {
		checks = append(checks, "postgres")
		return nil
	}), healthCheckerFunc(func(context.Context) error {
		checks = append(checks, "hmm")
		return hmmErr
	}), strategySet{router.StrategyCluster: true}, router.StrategyCluster)

	err := checker.CheckHealth(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, hmmErr)
	assert.Equal(t, []string{"postgres", "hmm"}, checks)
}

func TestReadinessCheckerWithoutHMM(t *testing.T) {
	checker := newReadinessChecker(databasePingerFunc(healthyDatabase), nil, strategySet{router.StrategyCluster: true}, router.StrategyCluster)

	assert.NoError(t, checker.CheckHealth(context.Background()))
}

func TestReadinessCheckerFailsWhenDefaultStrategyHasNoRouter(t *testing.T) {
	checker := newReadinessChecker(databasePingerFunc(healthyDatabase), nil, strategySet{router.StrategyCluster: true}, router.StrategyHMMEmbedding)

	err := checker.CheckHealth(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, router.ErrStrategyUnavailable)
	assert.ErrorContains(t, err, `default strategy "hmm_embedding"`)
}

func TestReadinessCheckerPassesWhenDefaultStrategyIsRoutable(t *testing.T) {
	checker := newReadinessChecker(databasePingerFunc(healthyDatabase), nil, strategySet{router.StrategyHMMEmbedding: true}, router.StrategyHMMEmbedding)

	assert.NoError(t, checker.CheckHealth(context.Background()))
}

var _ admin.HealthChecker = readinessChecker{}
