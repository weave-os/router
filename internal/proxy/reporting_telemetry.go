package proxy

import (
	"context"
	"weave-os/router/internal/auth"
)

func applyReportingTelemetry(ctx context.Context, params *InsertTelemetryParams, routed *turnLoopResult) {
	if params == nil {
		return
	}
	params.ExperimentSnapshotID = nil
	assignment, present := auth.ReportingAssignmentFrom(ctx)
	if !present || routed == nil || routed.Decision.Model == "" || routed.UsageBypass || routed.HardPinned || isUserForcedReason(routed.Decision.Reason) {
		return
	}
	if (assignment.Arm == auth.BlindExperimentArmPassthrough) != routed.CallerModelPassthrough {
		return
	}
	params.ExperimentSnapshotID = &assignment.SnapshotID
}
