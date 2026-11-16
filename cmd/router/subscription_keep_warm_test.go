package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
