package dispatch_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

func TestEffortResolutionMismatch(t *testing.T) {
	assert.False(t, dispatch.EffortResolution{Arm: "high", Sent: "high"}.Mismatch())
	assert.True(t, dispatch.EffortResolution{Arm: "xhigh", Sent: "high"}.Mismatch(), "a clamp credits the arm with a level it never bought")
	assert.True(t, dispatch.EffortResolution{Selected: "high", Sent: "medium"}.Mismatch())
	assert.False(t, dispatch.EffortResolution{}.Mismatch())
}

func TestEffortResolutionApplyEscalationWritesWireLevelOnly(t *testing.T) {
	opts := translate.EmitOptions{ForceEffort: "low"}

	dispatch.EffortResolution{Selected: "high", Sent: "high", Source: dispatch.EffortSourceEscalation}.Apply(&opts)

	assert.Empty(t, opts.ForceEffort)
	assert.Equal(t, "high", opts.ForceReasoningEffort)
}

func TestEarlyStopServedRestoreUndoesARedo(t *testing.T) {
	decision := router.Decision{Model: "stopped-model", Provider: providers.ProviderAnthropic}
	bindings := []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}
	marker, winnerIdx, fastServed := "stopped", 1, true
	var extractor *telemetry.UsageExtractor
	summary := translate.ResponseSummary{StopReason: "end_turn"}
	reqStats := providers.RequestMutationStats{CCOnlyToolsStripped: 2}
	effort := dispatch.EffortResolution{Arm: "high", Sent: "high"}
	served := &dispatch.EarlyStopServed{
		Decision: &decision, Bindings: &bindings, Marker: &marker, WinnerIdx: &winnerIdx, Extractor: &extractor,
		Summary: &summary, ReqStats: &reqStats, FastServed: &fastServed, Effort: &effort,
	}
	snapshot := served.Save()

	decision, bindings, marker, winnerIdx, fastServed = router.Decision{Model: "redo-model"}, nil, "redo", 0, false
	summary, reqStats, effort = translate.ResponseSummary{}, providers.RequestMutationStats{}, dispatch.EffortResolution{}
	served.Restore(snapshot)

	assert.Equal(t, "stopped-model", decision.Model)
	assert.Len(t, bindings, 1)
	assert.Equal(t, "stopped", marker)
	assert.Equal(t, 1, winnerIdx)
	assert.True(t, fastServed)
	assert.Equal(t, "end_turn", summary.StopReason)
	assert.Equal(t, 2, reqStats.CCOnlyToolsStripped)
	assert.Equal(t, "high", effort.Arm)
}
