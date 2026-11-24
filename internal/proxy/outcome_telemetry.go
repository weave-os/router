package proxy

import (
	"context"
	"weave-os/router/internal/routingpolicy"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/telemetry/respobserve"
	"weave-os/router/internal/telemetry/turnrecord"
	"weave-os/router/internal/translate"

	"github.com/tidwall/sjson"
)

func latestToolCallCountsJSON(env *translate.RequestEnvelope) []byte {
	if env.SourceFormat() != translate.FormatAnthropic {
		return nil
	}
	counts := respobserve.ToolErrorCountsJSON(respobserve.ToolErrorCounts(env.LatestToolCallOutcomes()))
	if counts == nil {
		return []byte(`{}`)
	}
	return counts
}

func applyServedGroupTelemetry(ports continuity.StrategyPorts, ctx context.Context, params *turnrecord.InsertTelemetryParams, routed routingpolicy.TurnRoute, served router.Decision) {
	group := ""
	if served.Model == routed.Decision.Model && !routingpolicy.IsUserForcedReason(routed.Decision.Reason) {
		group = routingpolicy.DecisionPolicyGroup(routed.Decision)
		if group == "" && routed.Decision.Metadata != nil && ports.Facts.ClassifierTelemetryOf(routed.Decision.Metadata).SelectionTrace != nil {
			group = ports.Facts.ClassifierTelemetryOf(routed.Decision.Metadata).SelectionTrace.SelectedGroup
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
