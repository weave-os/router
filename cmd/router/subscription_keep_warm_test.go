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
	policy, enabled, err := subscriptionKeepWarmPolicy("", "")
	require.NoError(t, err)
	require.False(t, enabled, "keep-warm is off unless explicitly enabled")
	require.Equal(t, defaultSubscriptionKeepWarmMaxAge, policy.MaxAge)
	require.Equal(t, int32(subscriptionKeepWarmBatchLimit), policy.BatchLimit)
	require.Equal(t, subscriptionKeepWarmSeatSpacing, policy.SeatSpacing)

	policy, enabled, err = subscriptionKeepWarmPolicy("true", " 72h ")
	require.NoError(t, err)
	require.True(t, enabled)
	require.Equal(t, 72*time.Hour, policy.MaxAge)

	for _, raw := range [][2]string{{"maybe", ""}, {"true", "10m"}, {"true", "soon"}} {
		_, _, err = subscriptionKeepWarmPolicy(raw[0], raw[1])
		require.Error(t, err, "%v", raw)
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
	t.Setenv("ROUTER_SUBSCRIPTION_KEEP_WARM_ENABLED", "true")
	ctx, cancel := context.WithCancel(context.Background())
	wait := startSubscriptionKeepWarm(ctx, subscriptions.NewRuntime(nil, nil, time.Now), slog.New(slog.DiscardHandler))
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	wait(shutdownCtx)
	require.NoError(t, shutdownCtx.Err(), "the sweep must exit once the process context is done")
}

func TestStartSubscriptionKeepWarmDisabledIsNoop(t *testing.T) {
	t.Setenv("ROUTER_SUBSCRIPTION_KEEP_WARM_ENABLED", "true")
	startSubscriptionKeepWarm(context.Background(), nil, slog.New(slog.DiscardHandler))(context.Background())
}
