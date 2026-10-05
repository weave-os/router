package usage_test

import (
	"net/http"
	"testing"
	"time"

	"weave-os/router/internal/proxy/usage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCodexHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "40")
	h.Set("x-codex-primary-window-minutes", "300")
	h.Set("x-codex-secondary-used-percent", "12.5")
	h.Set("x-codex-secondary-window-minutes", "10080")

	snap, ok := usage.ParseCodexHeaders(h)
	require.True(t, ok)
	assert.InDelta(t, 0.40, snap.Primary.UsedPercent, 1e-9)
	assert.Equal(t, 300, snap.Primary.WindowMinutes)
	assert.InDelta(t, 0.125, snap.Secondary.UsedPercent, 1e-9)
	assert.Equal(t, 10080, snap.Secondary.WindowMinutes)
}

func TestParseCodexHeaders_LowUsageNotMisread(t *testing.T) {
	// "1" means 1% used (max headroom), not 100% — must normalize to 0.01 so the
	// subsidy isn't silently wiped out at the moment the window is freshest.
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "1")
	h.Set("x-codex-primary-window-minutes", "300")
	snap, ok := usage.ParseCodexHeaders(h)
	require.True(t, ok)
	assert.InDelta(t, 0.01, snap.Primary.UsedPercent, 1e-9)
	// And that low usage yields a near-epsilon cost factor (covered model ~free).
	// A value above 100 clamps to fully used.
	h.Set("x-codex-primary-used-percent", "150")
	snap, _ = usage.ParseCodexHeaders(h)
	assert.InDelta(t, 1.0, snap.Primary.UsedPercent, 1e-9)
}

func TestParseCodexHeaders_NoneReportsFalse(t *testing.T) {
	_, ok := usage.ParseCodexHeaders(http.Header{})
	assert.False(t, ok)
}

// When Codex reports used-percent but omits window-minutes, the parser must
// still supply the known window length (primary ~5h, secondary weekly) so a
// near-cap reading stays authoritative for the window's life instead of aging
// out after the short ttl floor and re-subsidizing a still-capped credential.
func TestParseCodexHeaders_DefaultsWindowWhenOmitted(t *testing.T) {
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "80")
	h.Set("x-codex-secondary-used-percent", "97")
	snap, ok := usage.ParseCodexHeaders(h)
	require.True(t, ok)
	assert.Equal(t, 300, snap.Primary.WindowMinutes, "primary defaults to ~5h")
	assert.Equal(t, 10080, snap.Secondary.WindowMinutes, "secondary defaults to weekly")
}

func TestParseAnthropicUnified_FromRemainingLimit(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-5h-limit", "1000")
	h.Set("anthropic-ratelimit-unified-5h-remaining", "250") // 75% used
	h.Set("anthropic-ratelimit-unified-7d-limit", "100000")
	h.Set("anthropic-ratelimit-unified-7d-remaining", "90000") // 10% used

	snap, ok := usage.ParseAnthropicUnifiedHeaders(h)
	require.True(t, ok)
	assert.InDelta(t, 0.75, snap.Primary.UsedPercent, 1e-9)
	assert.InDelta(t, 0.10, snap.Secondary.UsedPercent, 1e-9)
}

// Utilization headers are 0-1 fractions on the wire, not 0-100 percents.
func TestParseAnthropicUnified_PrefersUtilization(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-5h-utilization", "0.63")
	h.Set("anthropic-ratelimit-unified-7d-utilization", "0.7370692663445869")
	snap, ok := usage.ParseAnthropicUnifiedHeaders(h)
	require.True(t, ok)
	assert.InDelta(t, 0.63, snap.Primary.UsedPercent, 1e-9)
	assert.InDelta(t, 0.7370692663445869, snap.Secondary.UsedPercent, 1e-9)
}

// Prod emits utilization above 1.0 while a window is served on overage credits
// (observed up to ~2.0). Clamp to 1.0 so Exhausted/CostFactor read "at cap".
func TestParseAnthropicUnified_OverageUtilizationClamped(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-5h-utilization", "1.28")
	snap, ok := usage.ParseAnthropicUnifiedHeaders(h)
	require.True(t, ok)
	assert.InDelta(t, 1.0, snap.Primary.UsedPercent, 1e-9)
	assert.True(t, snap.Exhausted())
}

func TestParseAnthropicUnified_OverageWithoutQuotaWindows(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-representative-claim", "overage")
	h.Set("anthropic-ratelimit-unified-overage-in-use", "true")
	h.Set("anthropic-ratelimit-unified-reset", "1790812800")
	snap, ok := usage.ParseAnthropicUnifiedHeaders(h)
	require.True(t, ok)
	assert.True(t, snap.OverageInUse)
	assert.Equal(t, int64(1790812800), snap.UnifiedResetAt.Unix())
	assert.True(t, snap.BillableOrExhausted())
	assert.False(t, snap.Exhausted(), "overage is billable, but the token can still serve")
}

func TestParseAnthropicUnified_OverageIncludedIsNotPaid(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-representative-claim", "seven_day_overage_included")
	h.Set("anthropic-ratelimit-unified-overage-in-use", "true")
	h.Set("anthropic-ratelimit-unified-7d_oi-utilization", "1.03")
	snapshot, ok := usage.ParseAnthropicUnifiedHeaders(h)
	require.True(t, ok)
	assert.False(t, snapshot.OverageInUse, "the special included claim has no verified paid-usage semantics")

	now := time.Unix(1790000000, 0)
	observer := usage.NewObserver([]byte("salt"), 10*time.Minute, func() time.Time { return now })
	key := observer.Key([]byte("sk-ant-oat01-token"))
	previouslyExhaustedPrimary := usage.Window{
		UsedPercent:   1.0,
		WindowMinutes: 5 * 60,
		ResetAt:       now.Add(time.Hour),
	}
	observer.Record(key, usage.Snapshot{
		Primary: previouslyExhaustedPrimary, RepresentativeClaim: usage.AnthropicClaimOverage, OverageInUse: true,
	})
	observer.Record(key, snapshot)
	latest, observed := observer.Snapshot(key)
	require.True(t, observed)
	assert.False(t, latest.OverageInUse, "a later included claim must clear the paid-lane observation")
	assert.Equal(t, previouslyExhaustedPrimary, latest.Primary, "an omitted 5h window must retain its last observation")
	assert.True(t, latest.ExhaustedAsOf(now), "the subscription remains exhausted until that 5h window is refreshed or resets")
}

// Prod traffic spells the long window "7d" (53k-row Phase 0 capture: zero
// "weekly" keys); "weekly" is kept as a legacy fallback only.
func TestParseAnthropicUnified_WeeklySpellingFallback(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-weekly-utilization", "0.42")
	snap, ok := usage.ParseAnthropicUnifiedHeaders(h)
	require.True(t, ok)
	assert.InDelta(t, 0.42, snap.Secondary.UsedPercent, 1e-9)

	// When both spellings appear, 7d (the prod spelling) wins.
	h.Set("anthropic-ratelimit-unified-7d-utilization", "0.90")
	snap, ok = usage.ParseAnthropicUnifiedHeaders(h)
	require.True(t, ok)
	assert.InDelta(t, 0.90, snap.Secondary.UsedPercent, 1e-9)
}

func TestObserver_RecordGetTTL(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	clock := func() time.Time { return now }
	o := usage.NewObserver([]byte("salt"), 10*time.Minute, clock)

	key := o.Key([]byte("sk-ant-oat01-abc"))
	o.Record(key, usage.Snapshot{Primary: usage.Window{UsedPercent: 0.5, WindowMinutes: 300}})

	got, ok := o.Snapshot(key)
	require.True(t, ok)
	assert.InDelta(t, 0.5, got.Primary.UsedPercent, 1e-9)

	// Empty observation must not be stored / must not clobber.
	o.Record(key, usage.Snapshot{})
	got, ok = o.Snapshot(key)
	require.True(t, ok)
	assert.InDelta(t, 0.5, got.Primary.UsedPercent, 1e-9)

	// A short idle gap (past the 10-min floor) must NOT drop a reading whose
	// quota window (5h) is still open — its headroom is still authoritative.
	now = now.Add(11 * time.Minute)
	_, ok = o.Snapshot(key)
	assert.True(t, ok, "a 5h-window reading survives a short idle gap")

	// Past the binding window → quota has reset → expired, dropped.
	now = now.Add(300 * time.Minute)
	_, ok = o.Snapshot(key)
	assert.False(t, ok)
}

// TestObserver_NearCapDoesNotResetToOptimistic is the regression for the
// reviewer-flagged bug: a credential observed near its cap must not age out after
// a short idle gap and then read as cold-start slack (which would re-subsidize a
// still-capped subscription). Its near-1.0 factor must persist for the life of
// the binding window, and only after that window resets should the entry drop so
// the cold-start path can legitimately treat it as never-observed again.
func TestObserver_NearCapDoesNotResetToOptimistic(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	clock := func() time.Time { return now }
	o := usage.NewObserver([]byte("salt"), 10*time.Minute, clock)
	key := o.Key([]byte("sk-ant-oat01-capped"))

	// Weekly window nearly exhausted.
	o.Record(key, usage.Snapshot{Secondary: usage.Window{UsedPercent: 0.98, WindowMinutes: 10080}})

	// 11 minutes later (past the old flat TTL): still observed, still ~full price.
	now = now.Add(11 * time.Minute)
	snap, ok := o.Snapshot(key)
	require.True(t, ok, "a near-cap reading must survive past the 10-min floor")
	assert.InDelta(t, 0.98, snap.Secondary.UsedPercent, 1e-9)

	// After the weekly window elapses, the quota has reset → drop → cold start.
	now = now.Add(10080 * time.Minute)
	_, ok = o.Snapshot(key)
	assert.False(t, ok, "after the binding window resets, the reading is no longer authoritative")
}

// TestObserver_LongWindowOutlivesShortBindingWindow guards the case where the 5h
// primary window is the more-utilized (binding) one but the weekly window is also
// near cap: the entry must survive past the 5h window so it does not reset to
// optimistic epsilon while weekly quota is still exhausted.
func TestObserver_LongWindowOutlivesShortBindingWindow(t *testing.T) {
	now := time.Unix(3_000_000, 0)
	clock := func() time.Time { return now }
	o := usage.NewObserver([]byte("salt"), 10*time.Minute, clock)
	key := o.Key([]byte("tok"))
	o.Record(key, usage.Snapshot{
		Primary:   usage.Window{UsedPercent: 0.99, WindowMinutes: 300},   // binds CostFactor
		Secondary: usage.Window{UsedPercent: 0.90, WindowMinutes: 10080}, // also near cap
	})

	// 6h later: past the 5h primary window, but the weekly window still binds.
	now = now.Add(6 * 60 * time.Minute)
	_, ok := o.Snapshot(key)
	assert.True(t, ok, "a near-cap weekly window keeps the entry alive past the 5h primary")
}

// TestObserver_SlackWindowDoesNotStrand is the converse: a 5h-capped reading whose
// weekly window is slack must expire at ~5h, not be held at full price for the
// (much longer) weekly window — otherwise a recovered primary quota would be
// stranded on cash/OSS for a week.
func TestObserver_SlackWindowDoesNotStrand(t *testing.T) {
	now := time.Unix(4_000_000, 0)
	clock := func() time.Time { return now }
	o := usage.NewObserver([]byte("salt"), 10*time.Minute, clock)
	key := o.Key([]byte("tok"))
	o.Record(key, usage.Snapshot{
		Primary:   usage.Window{UsedPercent: 0.99, WindowMinutes: 300},   // capped, 5h
		Secondary: usage.Window{UsedPercent: 0.05, WindowMinutes: 10080}, // slack
	})

	// Just past the 5h primary window: the slack weekly window must not keep the
	// stale primary-capped reading alive.
	now = now.Add(301 * time.Minute)
	_, ok := o.Snapshot(key)
	assert.False(t, ok, "a slack long window must not strand a recovered short-window quota")
}

func TestObserver_RecordMergesWindows(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	o := usage.NewObserver([]byte("salt"), 10*time.Minute, func() time.Time { return now })
	key := o.Key([]byte("tok"))

	// First response reports both windows; weekly is nearly exhausted.
	o.Record(key, usage.Snapshot{
		Primary:   usage.Window{UsedPercent: 0.10, WindowMinutes: 300},
		Secondary: usage.Window{UsedPercent: 0.95, WindowMinutes: 10080},
	})
	// A later response reports ONLY the primary window (secondary omitted).
	o.Record(key, usage.Snapshot{Primary: usage.Window{UsedPercent: 0.20, WindowMinutes: 300}})

	got, ok := o.Snapshot(key)
	require.True(t, ok)
	assert.InDelta(t, 0.20, got.Primary.UsedPercent, 1e-9, "primary updates")
	assert.InDelta(t, 0.95, got.Secondary.UsedPercent, 1e-9,
		"omitted secondary window must NOT be erased to slack")
}

func TestObserver_OverageRemainsUntilResetOrInPlanResponse(t *testing.T) {
	base := time.Unix(1_790_000_000, 0).UTC()
	clock := base
	observer := usage.NewObserver([]byte("salt"), 10*time.Minute, func() time.Time { return clock })
	key := observer.Key([]byte("sk-ant-oat01-overage"))
	observer.Record(key, usage.Snapshot{OverageInUse: true, UnifiedResetAt: base.Add(2 * time.Hour)})

	clock = base.Add(time.Hour)
	snap, ok := observer.Snapshot(key)
	require.True(t, ok)
	assert.True(t, snap.BillableOrExhausted())

	observer.Record(key, usage.Snapshot{Primary: usage.Window{UsedPercent: 0.10, WindowMinutes: 300}})
	snap, ok = observer.Snapshot(key)
	require.True(t, ok)
	assert.False(t, snap.BillableOrExhausted(), "a later in-plan observation clears overage")

	observer.Record(key, usage.Snapshot{OverageInUse: true, UnifiedResetAt: base.Add(2 * time.Hour)})
	clock = base.Add(3 * time.Hour)
	_, ok = observer.Snapshot(key)
	assert.False(t, ok, "the overage observation expires after the reported reset")
}

func TestObserver_OverageClearsAtResetWhileWeeklyWindowRemains(t *testing.T) {
	base := time.Unix(1_790_000_000, 0).UTC()
	clock := base
	observer := usage.NewObserver([]byte("salt"), 10*time.Minute, func() time.Time { return clock })
	key := observer.Key([]byte("sk-ant-oat01-overage"))
	observer.Record(key, usage.Snapshot{
		Secondary:      usage.Window{UsedPercent: 0.6, WindowMinutes: 7 * 24 * 60},
		OverageInUse:   true,
		UnifiedResetAt: base.Add(2 * time.Hour),
	})
	clock = base.Add(3 * time.Hour)
	snapshot, observed := observer.Snapshot(key)
	require.True(t, observed, "the weekly window remains authoritative")
	assert.False(t, snapshot.OverageInUse, "a prior overage response cannot persist beyond plan reset")
}

func TestObserver_OverageOnlyWithoutResetRequiresFreshHeadroom(t *testing.T) {
	base := time.Unix(1_790_000_000, 0).UTC()
	clock := base
	observer := usage.NewObserver([]byte("salt"), 10*time.Minute, func() time.Time { return clock })
	key := observer.Key([]byte("sk-ant-oat01-overage"))
	observer.Record(key, usage.Snapshot{OverageInUse: true})
	clock = base.Add(2 * time.Hour)
	snapshot, observed := observer.Snapshot(key)
	require.True(t, observed)
	assert.True(t, snapshot.OverageInUse)
	clock = base.Add(6 * time.Hour)
	observer.Sweep()
	snapshot, observed = observer.Snapshot(key)
	require.True(t, observed)
	assert.True(t, snapshot.OverageInUse)
	clock = base.Add(8 * 24 * time.Hour)
	observer.Record(key, usage.Snapshot{Primary: usage.Window{UsedPercent: 0.2, WindowMinutes: 300}})
	snapshot, observed = observer.Snapshot(key)
	require.True(t, observed)
	assert.False(t, snapshot.OverageInUse)
}

func TestObserver_DistinctTokensDistinctKeys(t *testing.T) {
	o := usage.NewObserver([]byte("salt"), time.Minute, func() time.Time { return time.Unix(1, 0) })
	assert.NotEqual(t, o.Key([]byte("token-a")), o.Key([]byte("token-b")))
	assert.Equal(t, o.Key([]byte("token-a")), o.Key([]byte("token-a")))
}

func TestSnapshot_Exhausted(t *testing.T) {
	t.Run("no data is never exhausted", func(t *testing.T) {
		assert.False(t, usage.Snapshot{}.Exhausted(),
			"absence of a reading is cold-start slack, not a spent plan")
	})
	t.Run("slack windows are not exhausted", func(t *testing.T) {
		s := usage.Snapshot{
			Primary:   usage.Window{UsedPercent: 0.50, WindowMinutes: 300},
			Secondary: usage.Window{UsedPercent: 0.95, WindowMinutes: 10080},
		}
		assert.False(t, s.Exhausted(), "95% still has headroom — the token can still serve")
	})
	t.Run("weekly window at cap is exhausted", func(t *testing.T) {
		s := usage.Snapshot{
			Primary:   usage.Window{UsedPercent: 0.10, WindowMinutes: 300},
			Secondary: usage.Window{UsedPercent: 1.0, WindowMinutes: 10080},
		}
		assert.True(t, s.Exhausted(), "a bound weekly window means the upstream 429s")
	})
	t.Run("primary (5h) window at cap is exhausted", func(t *testing.T) {
		s := usage.Snapshot{Primary: usage.Window{UsedPercent: 1.0, WindowMinutes: 300}}
		assert.True(t, s.Exhausted())
	})
	t.Run("rounding just under 1.0 still reads exhausted", func(t *testing.T) {
		s := usage.Snapshot{Secondary: usage.Window{UsedPercent: 0.999, WindowMinutes: 10080}}
		assert.True(t, s.Exhausted(),
			"integer-percent rounding at the cap must not read as headroom")
	})
}

func TestSnapshot_ExhaustedAsOfHonorsResetWindows(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := usage.Snapshot{
		Primary:    usage.Window{UsedPercent: 1.0, WindowMinutes: 300, ResetAt: now.Add(-time.Minute)},
		Secondary:  usage.Window{UsedPercent: 0.10, WindowMinutes: 10080},
		ObservedAt: now.Add(-time.Hour),
	}
	assert.False(t, s.ExhaustedAsOf(now),
		"an expired primary cap must not keep the account exhausted while the weekly window has slack")
	assert.True(t, s.Exhausted(), "Exhausted() is evaluated at ObservedAt, when the primary window was still spent")
}
func TestParseAnthropicUnifiedHeaders_ResetAt(t *testing.T) {
	t.Run("RFC3339 reset", func(t *testing.T) {
		h := http.Header{}
		h.Set("anthropic-ratelimit-unified-7d-utilization", "1.0")
		h.Set("anthropic-ratelimit-unified-7d-reset", "2026-06-28T03:00:00Z")
		snap, ok := usage.ParseAnthropicUnifiedHeaders(h)
		require.True(t, ok)
		assert.Equal(t, "2026-06-28T03:00:00Z", snap.Secondary.ResetAt.Format(time.RFC3339))
	})
	t.Run("unix-seconds reset fallback", func(t *testing.T) {
		h := http.Header{}
		h.Set("anthropic-ratelimit-unified-5h-utilization", "0.90")
		h.Set("anthropic-ratelimit-unified-5h-reset", "1782702000")
		snap, ok := usage.ParseAnthropicUnifiedHeaders(h)
		require.True(t, ok)
		assert.Equal(t, int64(1782702000), snap.Primary.ResetAt.Unix())
	})
	t.Run("absent reset leaves zero", func(t *testing.T) {
		h := http.Header{}
		h.Set("anthropic-ratelimit-unified-7d-utilization", "0.50")
		snap, ok := usage.ParseAnthropicUnifiedHeaders(h)
		require.True(t, ok)
		assert.True(t, snap.Secondary.ResetAt.IsZero())
	})
}

// TestObserver_ResetAtExpiresBeforeWindowLength is the failover re-probe fix: an
// exhausted weekly reading whose upstream reset is hours away must expire at that
// reset, NOT a full 7-day window length from when it was observed — otherwise the
// exhaustion suppression strands the subscription on the Weave key for days after
// the plan has already refilled.
func TestObserver_ResetAtExpiresBeforeWindowLength(t *testing.T) {
	base := time.Unix(1_800_000_000, 0).UTC()
	clock := base
	o := usage.NewObserver([]byte("salt"), 10*time.Minute, func() time.Time { return clock })
	key := o.Key([]byte("tok"))

	// Exhausted weekly window, but the plan resets in 2 hours.
	o.Record(key, usage.Snapshot{
		Secondary: usage.Window{UsedPercent: 1.0, WindowMinutes: 10080, ResetAt: base.Add(2 * time.Hour)},
	})

	// 1 hour later (before reset): still authoritative → still exhausted.
	clock = base.Add(1 * time.Hour)
	snap, ok := o.Snapshot(key)
	require.True(t, ok, "reading must survive until its reset")
	assert.True(t, snap.Exhausted())

	// 3 hours later (past reset): evicted, so the credential reads as never-observed
	// and the next turn re-probes on the subscription instead of staying suppressed.
	clock = base.Add(3 * time.Hour)
	_, ok = o.Snapshot(key)
	assert.False(t, ok, "reading must expire at the reset, not 7 days after observation")
}

func TestObserver_NoResetFallsBackToWindowLength(t *testing.T) {
	base := time.Unix(1_800_000_000, 0).UTC()
	clock := base
	o := usage.NewObserver([]byte("salt"), 10*time.Minute, func() time.Time { return clock })
	key := o.Key([]byte("tok"))
	// Exhausted weekly window, no reset reported → retained for the full week.
	o.Record(key, usage.Snapshot{Secondary: usage.Window{UsedPercent: 1.0, WindowMinutes: 10080}})

	clock = base.Add(6 * 24 * time.Hour) // 6 days: still inside the 7-day window
	_, ok := o.Snapshot(key)
	assert.True(t, ok, "with no reset header the window-length horizon still applies")
}
