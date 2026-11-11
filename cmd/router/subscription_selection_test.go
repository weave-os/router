package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/proxy/usage"
)

func TestSubscriptionCandidateSignalUsesNextExpiryAndBindingUtilization(t *testing.T) {
	now := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	weeklyReset := now.Add(7 * 24 * time.Hour)
	signal := subscriptionCandidateSignal(usage.Snapshot{
		Primary:   usage.Window{UsedPercent: 0.4, WindowMinutes: 300, ResetAt: now.Add(5 * time.Minute)},
		Secondary: usage.Window{UsedPercent: 0.8, WindowMinutes: 10080, ResetAt: weeklyReset},
	}, now)

	require.True(t, signal.Observed)
	require.Equal(t, 0.8, signal.UsedFraction)
	require.Equal(t, now.Add(5*time.Minute), signal.ResetAt)
}

func TestSubscriptionCandidateSignalUsesEarlierResetWhenUtilizationTies(t *testing.T) {
	now := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	earlierReset := now.Add(time.Hour)
	laterReset := now.Add(2 * time.Hour)
	signal := subscriptionCandidateSignal(usage.Snapshot{
		Primary:   usage.Window{UsedPercent: 0.7, WindowMinutes: 300, ResetAt: laterReset},
		Secondary: usage.Window{UsedPercent: 0.7, WindowMinutes: 10080, ResetAt: earlierReset},
	}, now)

	require.True(t, signal.Observed)
	require.Equal(t, 0.7, signal.UsedFraction)
	require.Equal(t, earlierReset, signal.ResetAt)
}

func TestSubscriptionCandidateSignalRejectsInvalidUtilization(t *testing.T) {
	now := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	signal := subscriptionCandidateSignal(usage.Snapshot{
		Primary: usage.Window{UsedPercent: 1.1, WindowMinutes: 300, ResetAt: now.Add(time.Hour)},
	}, now)

	require.False(t, signal.Observed)
}

func TestSubscriptionCandidateSignalUsesUnifiedResetForBillableObservation(t *testing.T) {
	now := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	unifiedReset := now.Add(2 * time.Hour)
	signal := subscriptionCandidateSignal(usage.Snapshot{
		Primary:        usage.Window{UsedPercent: 0.9, WindowMinutes: 300, ResetAt: now.Add(5 * time.Minute)},
		OverageInUse:   true,
		UnifiedResetAt: unifiedReset,
	}, now)

	require.True(t, signal.Observed)
	require.True(t, signal.Billable)
	require.Equal(t, 1.0, signal.UsedFraction)
	require.Equal(t, unifiedReset, signal.ResetAt)
}

func TestSubscriptionCandidateSignalLeavesClaimOnlyObservationUnknown(t *testing.T) {
	signal := subscriptionCandidateSignal(usage.Snapshot{RepresentativeClaim: usage.AnthropicClaimFiveHour}, time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC))

	require.False(t, signal.Observed)
}

func TestSubscriptionCandidateSignalIgnoresExpiredWindow(t *testing.T) {
	now := time.Date(2026, time.November, 11, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(time.Hour)
	signal := subscriptionCandidateSignal(usage.Snapshot{ObservedAt: now, Primary: usage.Window{UsedPercent: 1, WindowMinutes: 300, ResetAt: now.Add(-time.Minute)}, Secondary: usage.Window{UsedPercent: .4, WindowMinutes: 10080, ResetAt: resetAt}}, now)
	require.True(t, signal.Observed)
	require.False(t, signal.Exhausted)
	require.Equal(t, .4, signal.UsedFraction)
	require.Equal(t, resetAt, signal.ResetAt)
	require.Equal(t, now, signal.ObservedAt)
}

func TestSubscriptionCandidateSignalInfersExpiryFromWindowObservation(t *testing.T) {
	now := time.Date(2026, time.November, 11, 12, 0, 0, 0, time.UTC)
	primaryObserved := now.Add(-4 * time.Hour)
	signal := subscriptionCandidateSignal(usage.Snapshot{ObservedAt: now, Primary: usage.Window{UsedPercent: .2, WindowMinutes: 300, ObservedAt: primaryObserved}, Secondary: usage.Window{UsedPercent: .8, WindowMinutes: 10080}}, now)
	require.Equal(t, now.Add(time.Hour), signal.ResetAt)
	require.Equal(t, .8, signal.UsedFraction)
}
