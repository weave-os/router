package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStartupWaitsForPostgresBeyondInitialConnectDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		attempts := 0
		err := waitForStartupPostgres(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), func(ctx context.Context) error {
			attempts++
			if time.Since(started) < 20*time.Second {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		})
		require.NoError(t, err)
		require.Equal(t, 5, attempts)
		require.Equal(t, 24*time.Second, time.Since(started))
	})
}

func TestStartupPostgresBudgetAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name          string
		cancelAfter   time.Duration
		expectedWait  time.Duration
		expectedError error
		blockingPing  bool
	}{
		{name: "connection errors exhaust startup budget", expectedWait: 120 * time.Second, expectedError: context.DeadlineExceeded},
		{name: "hung connects exhaust startup budget", expectedWait: 120 * time.Second, expectedError: context.DeadlineExceeded, blockingPing: true},
		{name: "shutdown cancels backoff", cancelAfter: 2500 * time.Millisecond, expectedWait: 2500 * time.Millisecond, expectedError: context.Canceled},
		{name: "shutdown cancels connect", cancelAfter: 2500 * time.Millisecond, expectedWait: 2500 * time.Millisecond, expectedError: context.Canceled, blockingPing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if test.cancelAfter > 0 {
					time.AfterFunc(test.cancelAfter, cancel)
				}
				started := time.Now()
				err := waitForStartupPostgres(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), func(attemptCtx context.Context) error {
					if test.blockingPing {
						<-attemptCtx.Done()
						return attemptCtx.Err()
					}
					return errors.New("connection refused")
				})
				require.ErrorIs(t, err, test.expectedError)
				require.ErrorContains(t, err, "gateway Postgres startup")
				require.Equal(t, test.expectedWait, time.Since(started))
			})
		})
	}
}

func TestStartupPostgresReadyImmediately(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		err := waitForStartupPostgres(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context) error { return nil })
		require.NoError(t, err)
		require.Equal(t, time.Duration(0), time.Since(started))
	})
}

func TestGatewayStartupProbeIsLatchedAndDoesNotRepeatInitialization(t *testing.T) {
	var ready atomic.Bool
	probe := startupHandler(ready.Load)
	response := httptest.NewRecorder()
	probe.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/startupz", nil))
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	ready.Store(true)
	for range 3 {
		response = httptest.NewRecorder()
		probe.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/startupz", nil))
		require.Equal(t, http.StatusOK, response.Code)
	}
}

func TestGatewayStartupWaitsForActivationAndHonorsBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err := waitForStartupWorker(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context) error { return errors.New("activation not present") })
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
}
