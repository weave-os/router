package proxy

import (
	"math"
	"time"

	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/subscriptions/entitlement"
)

const (
	subscriptionSourceOptimizerVersion = "boost-v1"
	sourcePressureSafetyMargin         = 0.1
	unknownLinkedSourcePressure        = 1.0
)

func preferIncludedRouter(coverage entitlement.Coverage, snapshot usage.Snapshot, observed bool, now time.Time) bool {
	includedPressure, ok := includedRouterPressure(coverage, now)
	if !ok {
		return false
	}
	linkedPressure := unknownLinkedSourcePressure
	if observed {
		linkedPressure = linkedSubscriptionPressure(snapshot, now)
	}
	return includedPressure+sourcePressureSafetyMargin < linkedPressure
}

func linkedSubscriptionResetAt(snapshot usage.Snapshot, now time.Time) time.Time {
	var resetAt time.Time
	for _, window := range []usage.Window{snapshot.Primary, snapshot.Secondary} {
		if window.UsedPercent < 0.999 {
			continue
		}
		candidate := window.ResetAt
		if candidate.IsZero() && window.WindowMinutes > 0 {
			candidate = snapshot.ObservedAt.Add(time.Duration(window.WindowMinutes) * time.Minute)
		}
		if candidate.After(resetAt) {
			resetAt = candidate
		}
	}
	if !resetAt.After(now) {
		return now.Add(time.Minute)
	}
	return resetAt
}

func includedRouterPressure(coverage entitlement.Coverage, now time.Time) (float64, bool) {
	billing, billingOK := normalizedDepletionPressure(
		coverage.BillingUsedUsdMicros+coverage.ProjectedUsdMicros,
		coverage.BillingLimitUsdMicros,
		coverage.BillingPeriod.Start,
		coverage.BillingPeriod.End,
		now,
	)
	sixHour, sixHourOK := normalizedDepletionPressure(
		coverage.SixHourUsedUsdMicros+coverage.ProjectedUsdMicros,
		coverage.SixHourLimitUsdMicros,
		coverage.SixHourPeriod.Start,
		coverage.SixHourPeriod.End,
		now,
	)
	switch {
	case billingOK && sixHourOK:
		return math.Max(billing, sixHour), true
	case billingOK:
		return billing, true
	case sixHourOK:
		return sixHour, true
	default:
		return 0, false
	}
}

func linkedSubscriptionPressure(snapshot usage.Snapshot, now time.Time) float64 {
	pressures := make([]float64, 0, 2)
	for _, window := range []usage.Window{snapshot.Primary, snapshot.Secondary} {
		if window.WindowMinutes <= 0 && window.UsedPercent <= 0 {
			continue
		}
		if !window.ResetAt.IsZero() && !window.ResetAt.After(now) {
			pressures = append(pressures, 0)
			continue
		}
		pressure := math.Max(0, window.UsedPercent)
		if window.WindowMinutes > 0 && !window.ResetAt.IsZero() {
			start := window.ResetAt.Add(-time.Duration(window.WindowMinutes) * time.Minute)
			if normalized, ok := normalizedDepletionPressure(
				int64(pressure*1_000_000),
				1_000_000,
				start,
				window.ResetAt,
				snapshot.ObservedAt,
			); ok {
				pressure = normalized
			}
		}
		pressures = append(pressures, pressure)
	}
	if len(pressures) == 0 {
		return unknownLinkedSourcePressure
	}
	return slicesMax(pressures)
}

func normalizedDepletionPressure(used, limit int64, start, end, now time.Time) (float64, bool) {
	if limit <= 0 || start.IsZero() || !start.Before(end) {
		return 0, false
	}
	elapsed := now.Sub(start).Seconds() / end.Sub(start).Seconds()
	elapsed = math.Min(1, math.Max(0.01, elapsed))
	usedFraction := math.Max(0, float64(used)/float64(limit))
	return usedFraction / elapsed, true
}

func slicesMax(values []float64) float64 {
	maximum := values[0]
	for _, value := range values[1:] {
		maximum = math.Max(maximum, value)
	}
	return maximum
}
