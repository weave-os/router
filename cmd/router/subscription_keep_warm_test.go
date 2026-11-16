package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/subscriptions"
)

func TestSubscriptionKeepWarmPolicy(t *testing.T) {
	policy, err := subscriptionKeepWarmPolicy("")
	require.NoError(t, err)
	require.Equal(t, defaultSubscriptionKeepWarmMaxAge, policy.MaxAge)
	require.Equal(t, int32(subscriptionKeepWarmBatchLimit), policy.BatchLimit)
	require.Equal(t, subscriptionKeepWarmSeatSpacing, policy.SeatSpacing)

	policy, err = subscriptionKeepWarmPolicy(" 72h ")
	require.NoError(t, err)
	require.Equal(t, 72*time.Hour, policy.MaxAge)

	for _, raw := range []string{"10m", "soon"} {
		_, err = subscriptionKeepWarmPolicy(raw)
		require.Error(t, err, raw)
	}
}

func TestJitteredKeepWarmDelayStaysInRange(t *testing.T) {
	for range 100 {
		delay := jitteredKeepWarmDelay(time.Minute, time.Minute)
		require.GreaterOrEqual(t, delay, time.Minute)
		require.LessOrEqual(t, delay, 2*time.Minute)
	}
}

func TestStartSubscriptionKeepWarmStopsWithProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	wait := startSubscriptionKeepWarm(ctx, subscriptions.NewRuntime(nil, nil, time.Now), slog.New(slog.DiscardHandler))
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	wait(shutdownCtx)
	require.NoError(t, shutdownCtx.Err(), "the sweep must exit once the process context is done")
}

func TestStartSubscriptionKeepWarmWithoutPoolsIsNoop(t *testing.T) {
	require.NotPanics(t, func() {
		startSubscriptionKeepWarm(context.Background(), nil, slog.New(slog.DiscardHandler))(context.Background())
	})
}
