package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/api/admin"
)

type databasePingerFunc func(context.Context) error

func (f databasePingerFunc) Ping(ctx context.Context) error {
	return f(ctx)
}

type healthCheckerFunc func(context.Context) error

func (f healthCheckerFunc) CheckHealth(ctx context.Context) error {
	return f(ctx)
}

func TestReadinessCheckerRequiresDatabase(t *testing.T) {
	databaseErr := errors.New("connection reset by peer")
	checker := newReadinessChecker(databasePingerFunc(func(context.Context) error {
		return databaseErr
	}), healthCheckerFunc(func(context.Context) error {
		t.Fatal("HMM readiness should not run when PostgreSQL is unavailable")
		return nil
	}))

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
	}))

	err := checker.CheckHealth(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, hmmErr)
	assert.Equal(t, []string{"postgres", "hmm"}, checks)
}

func TestReadinessCheckerWithoutHMM(t *testing.T) {
	checker := newReadinessChecker(databasePingerFunc(func(context.Context) error {
		return nil
	}), nil)

	assert.NoError(t, checker.CheckHealth(context.Background()))
}

var _ admin.HealthChecker = readinessChecker{}
