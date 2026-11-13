package proxy

import (
	"context"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/tidwall/sjson"
)

func latestToolCallCountsJSON(env *translate.RequestEnvelope) []byte {
	if env.SourceFormat() != translate.FormatAnthropic {
		return nil
	}
	counts := toolErrorCountsJSON(toolErrorCounts(env.LatestToolCallOutcomes()))
	if counts == nil {
		return []byte(`{}`)
	}
	return counts
}

// selection_trace can describe a fresh recommendation on a sticky turn. Keep
// the served group separate; a rescue to a different model has no known group.
func applyServedGroupTelemetry(ctx context.Context, params *InsertTelemetryParams, routed turnLoopResult, served router.Decision) {
	group := ""
	if served.Model == routed.Decision.Model && !isUserForcedReason(routed.Decision.Reason) {
		group = decisionPolicyGroup(routed.Decision)
		if group == "" && routed.Decision.Metadata != nil && routed.Decision.Metadata.SelectionTrace != nil {
			group = routed.Decision.Metadata.SelectionTrace.SelectedGroup
		}
		if group == "" && routed.StickyHit && served.Model == routed.PinModel {
			group = routed.PinPolicyGroup
		}
	}
	trace := params.SelectionTrace
	if len(trace) == 0 {
		trace = []byte(`{}`)
	}
	encoded, err := sjson.SetBytes(trace, "served_group", group)
	if err != nil {
		observability.FromContext(ctx).Error("Failed to record served routing group", "err", err)
		return
	}
	params.SelectionTrace = encoded
}
