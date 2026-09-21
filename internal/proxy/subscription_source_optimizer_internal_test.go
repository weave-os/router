package proxy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/subscriptions/entitlement"
)

func TestPreferIncludedRouterUsesNormalizedDepletionPressure(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	coverage := entitlement.Coverage{
		BillingPeriod:         entitlement.Period{Start: now.Add(-15 * 24 * time.Hour), End: now.Add(15 * 24 * time.Hour)},
		SixHourPeriod:         entitlement.Period{Start: now.Add(-3 * time.Hour), End: now.Add(3 * time.Hour)},
		BillingLimitUsdMicros: 200_000_000,
		SixHourLimitUsdMicros: 20_000_000,
		BillingUsedUsdMicros:  20_000_000,
		SixHourUsedUsdMicros:  2_000_000,
		ProjectedUsdMicros:    100_000,
	}
	linked := usage.Snapshot{
		Primary: usage.Window{
			UsedPercent:   0.8,
			WindowMinutes: 360,
			ResetAt:       now.Add(3 * time.Hour),
		},
		ObservedAt: now,
	}

	assert.True(t, preferIncludedRouter(coverage, linked, true, now))

	coverage.SixHourUsedUsdMicros = 18_000_000
	assert.False(t, preferIncludedRouter(coverage, linked, true, now))
}

func TestPreferIncludedRouterTreatsUnknownLinkedUsageConservatively(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	coverage := entitlement.Coverage{
		BillingPeriod:         entitlement.Period{Start: now.Add(-15 * 24 * time.Hour), End: now.Add(15 * 24 * time.Hour)},
		SixHourPeriod:         entitlement.Period{Start: now.Add(-3 * time.Hour), End: now.Add(3 * time.Hour)},
		BillingLimitUsdMicros: 200_000_000,
		SixHourLimitUsdMicros: 20_000_000,
		BillingUsedUsdMicros:  20_000_000,
		SixHourUsedUsdMicros:  2_000_000,
	}

	assert.True(t, preferIncludedRouter(coverage, usage.Snapshot{}, false, now))
}

func TestLinkedSubscriptionResetAtWaitsForEveryExhaustedWindow(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	snapshot := usage.Snapshot{
		Primary:    usage.Window{UsedPercent: 1, ResetAt: now.Add(time.Hour)},
		Secondary:  usage.Window{UsedPercent: 1, ResetAt: now.Add(4 * time.Hour)},
		ObservedAt: now,
	}

	assert.Equal(t, now.Add(4*time.Hour), linkedSubscriptionResetAt(snapshot, now))
}

func TestLinkedSubscriptionResetAtPreservesNearTermReset(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	snapshot := usage.Snapshot{
		Primary:    usage.Window{UsedPercent: 1, ResetAt: now.Add(15 * time.Second)},
		ObservedAt: now,
	}

	assert.Equal(t, now.Add(15*time.Second), linkedSubscriptionResetAt(snapshot, now))
}

func TestPreferIncludedRouterIgnoresExpiredLinkedWindow(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	coverage := entitlement.Coverage{
		BillingPeriod:         entitlement.Period{Start: now.Add(-15 * 24 * time.Hour), End: now.Add(15 * 24 * time.Hour)},
		SixHourPeriod:         entitlement.Period{Start: now.Add(-3 * time.Hour), End: now.Add(3 * time.Hour)},
		BillingLimitUsdMicros: 200_000_000,
		SixHourLimitUsdMicros: 20_000_000,
		BillingUsedUsdMicros:  20_000_000,
		SixHourUsedUsdMicros:  2_000_000,
	}
	linked := usage.Snapshot{
		Primary: usage.Window{
			UsedPercent: 1, WindowMinutes: 300, ResetAt: now.Add(-time.Minute),
		},
		ObservedAt: now,
	}

	assert.False(t, preferIncludedRouter(coverage, linked, true, now))
}
