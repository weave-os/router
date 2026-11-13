package proxy

import (
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sidecarStatsInt64Ptr(v int64) *int64 { return &v }

// TestDecisionSpan_SidecarStats_AllFivePresent pins that all five serving-stats
// attrs carry their exact values, with no rounding/truncation across the boundary.
func TestDecisionSpan_SidecarStats_AllFivePresent(t *testing.T) {
	collector := newBypassSpanCollector(t)
	svc := newEmbedTestService(t, collector, &embedTestRouter{metadata: &router.RoutingMetadata{SidecarStats: &router.SidecarServingStats{
		EmbedCacheHits:      sidecarStatsInt64Ptr(1009),
		EmbedCacheMisses:    sidecarStatsInt64Ptr(37),
		EmbedCacheEvictions: sidecarStatsInt64Ptr(5),
		RoutesInflight:      sidecarStatsInt64Ptr(3),
		OverrunsLive:        sidecarStatsInt64Ptr(2),
	}}}, nil)

	sp := decisionSpanEmbed(t, svc, collector)

	hits, hitsPresent := spanEmbedInt(sp, "routing.embed_cache_hits")
	require.True(t, hitsPresent, "EmbedCacheHits must carry routing.embed_cache_hits")
	assert.Equal(t, int64(1009), hits)

	misses, missesPresent := spanEmbedInt(sp, "routing.embed_cache_misses")
	require.True(t, missesPresent, "EmbedCacheMisses must carry routing.embed_cache_misses")
	assert.Equal(t, int64(37), misses)

	evictions, evictionsPresent := spanEmbedInt(sp, "routing.embed_cache_evictions")
	require.True(t, evictionsPresent, "EmbedCacheEvictions must carry routing.embed_cache_evictions")
	assert.Equal(t, int64(5), evictions)

	inflight, inflightPresent := spanEmbedInt(sp, "routing.sidecar_inflight")
	require.True(t, inflightPresent, "RoutesInflight must carry routing.sidecar_inflight")
	assert.Equal(t, int64(3), inflight)

	overruns, overrunsPresent := spanEmbedInt(sp, "routing.sidecar_overruns_live")
	require.True(t, overrunsPresent, "OverrunsLive must carry routing.sidecar_overruns_live")
	assert.Equal(t, int64(2), overruns)

	_, routePresent := spanEmbedInt(sp, "latency.route_ms")
	assert.True(t, routePresent, "the established route_ms attribute must stay present")
}

// TestDecisionSpan_SidecarStats_PresentZeroIsEmittedAsZero pins that a measured
// zero (warm embed cache, no misses) is emitted as a PRESENT 0 — distinct from absence.
func TestDecisionSpan_SidecarStats_PresentZeroIsEmittedAsZero(t *testing.T) {
	collector := newBypassSpanCollector(t)
	svc := newEmbedTestService(t, collector, &embedTestRouter{metadata: &router.RoutingMetadata{SidecarStats: &router.SidecarServingStats{
		EmbedCacheMisses: sidecarStatsInt64Ptr(0),
	}}}, nil)

	sp := decisionSpanEmbed(t, svc, collector)

	misses, present := spanEmbedInt(sp, "routing.embed_cache_misses")
	require.True(t, present, "a measured zero miss count must still be emitted as a present 0, not dropped")
	assert.Zero(t, misses)
}

// TestDecisionSpan_SidecarStats_Absent covers nil Metadata, nil SidecarStats,
// and a partial SidecarStats (only the set field must appear).
func TestDecisionSpan_SidecarStats_Absent(t *testing.T) {
	allFiveKeys := []string{
		"routing.embed_cache_hits",
		"routing.embed_cache_misses",
		"routing.embed_cache_evictions",
		"routing.sidecar_inflight",
		"routing.sidecar_overruns_live",
	}
	for _, tc := range []struct {
		name         string
		metadata     *router.RoutingMetadata
		presentKey   string
		presentValue int64
	}{
		{name: "nil metadata", metadata: nil},
		{name: "metadata without sidecar stats", metadata: &router.RoutingMetadata{}},
		{
			name: "sidecar stats with only inflight set",
			metadata: &router.RoutingMetadata{SidecarStats: &router.SidecarServingStats{
				RoutesInflight: sidecarStatsInt64Ptr(4),
			}},
			presentKey:   "routing.sidecar_inflight",
			presentValue: 4,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collector := newBypassSpanCollector(t)
			svc := newEmbedTestService(t, collector, &embedTestRouter{metadata: tc.metadata}, nil)

			sp := decisionSpanEmbed(t, svc, collector)

			for _, key := range allFiveKeys {
				if key == tc.presentKey {
					got, present := spanEmbedInt(sp, key)
					require.True(t, present, "%s was explicitly set and must be present", key)
					assert.Equal(t, tc.presentValue, got)
					continue
				}
				_, present := spanEmbedInt(sp, key)
				assert.False(t, present, "%s was never set and must not be emitted", key)
			}

			_, routePresent := spanEmbedInt(sp, "latency.route_ms")
			assert.True(t, routePresent, "the span must still carry latency.route_ms regardless of sidecar stats")
		})
	}
}

// TestDecisionSpan_SidecarStats_EmittedWhenTimingsNil guards the independence
// of the two Metadata guards: a nil SidecarTimings must not suppress SidecarStats.
func TestDecisionSpan_SidecarStats_EmittedWhenTimingsNil(t *testing.T) {
	collector := newBypassSpanCollector(t)
	svc := newEmbedTestService(t, collector, &embedTestRouter{metadata: &router.RoutingMetadata{
		SidecarTimings: nil,
		SidecarStats: &router.SidecarServingStats{
			EmbedCacheHits: sidecarStatsInt64Ptr(11),
		},
	}}, nil)

	sp := decisionSpanEmbed(t, svc, collector)

	hits, hitsPresent := spanEmbedInt(sp, "routing.embed_cache_hits")
	require.True(t, hitsPresent, "SidecarStats must be emitted even when SidecarTimings is nil — the two guards are independent")
	assert.Equal(t, int64(11), hits)

	_, embedMsPresent := spanEmbedInt(sp, "latency.embed_ms")
	assert.False(t, embedMsPresent, "nil SidecarTimings must not fabricate latency.embed_ms")
}

// TestDecisionSpan_SidecarStats_SurvivesPlannerStay mirrors
// TestDecisionSpan_EmbedMs_SurvivesPlannerStay: a STAY turn replaces the served
// decision with a pin (nil Metadata), so stats must come from Fresh, not Served.
func TestDecisionSpan_SidecarStats_SurvivesPlannerStay(t *testing.T) {
	collector := newBypassSpanCollector(t)
	store := newStubPinStore()
	store.getFound = true
	store.getPin = sessionpin.Pin{
		Provider:    providers.ProviderAnthropic,
		Model:       "claude-haiku-4-5",
		Reason:      "cluster",
		PinnedUntil: time.Now().Add(time.Hour),
	}
	svc := newEmbedTestService(t, collector, &embedTestRouter{metadata: &router.RoutingMetadata{SidecarStats: &router.SidecarServingStats{
		EmbedCacheHits:   sidecarStatsInt64Ptr(21),
		EmbedCacheMisses: sidecarStatsInt64Ptr(0),
	}}}, store)

	sp := decisionSpanEmbed(t, svc, collector)

	require.Equal(t, "stay", spanStr(t, sp, "planner.outcome"), "pin must be served via a planner STAY for this test to pin anything")
	hits, hitsPresent := spanEmbedInt(sp, "routing.embed_cache_hits")
	require.True(t, hitsPresent, "a STAY turn still ran the sidecar this turn — dropping stats undercounts serving load on sticky sessions")
	assert.Equal(t, int64(21), hits)
	misses, missesPresent := spanEmbedInt(sp, "routing.embed_cache_misses")
	require.True(t, missesPresent, "a present zero must survive STAY the same as a present non-zero")
	assert.Zero(t, misses)
}
