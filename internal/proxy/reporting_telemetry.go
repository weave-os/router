package proxy

import (
	"context"
	"weave-os/router/internal/auth"
)

// ReportingProvenanceSchemaVersion identifies the persisted reporting contract.
const ReportingProvenanceSchemaVersion int16 = 1

func applyReportingTelemetry(ctx context.Context, params *InsertTelemetryParams, routed *turnLoopResult) {
	if params == nil {
		return
	}
	assignment, present := auth.ReportingAssignmentFrom(ctx)
	if !present {
		return
	}
	version := ReportingProvenanceSchemaVersion
	params.ReportingSchemaVersion = &version
	params.ReportingMode = assignment.Mode
	params.ReportingExperimentID = assignment.ExperimentID
	params.ReportingRevision = &assignment.Revision
	params.ReportingAssignedArm = assignment.Arm
	params.ReportingSubjectKey = assignment.SubjectKey
	applied := false
	switch {
	case routed == nil || routed.Decision.Model == "":
		params.ReportingBypassReason = auth.CohortBypassNotDispatched
	case routed.UsageBypass:
		params.ReportingBypassReason = auth.CohortBypassUsageBypass
	case isUserForcedReason(routed.Decision.Reason):
		params.ReportingBypassReason = auth.CohortBypassForceModel
	case routed.HardPinned:
		params.ReportingBypassReason = auth.CohortBypassHardPin
	case assignment.Arm == auth.BlindExperimentArmPassthrough:
		applied = routed.CallerModelPassthrough
		if !applied {
			params.ReportingBypassReason = auth.CohortBypassNotDispatched
		}
	default:
		applied = !routed.CallerModelPassthrough
		if !applied {
			params.ReportingBypassReason = auth.CohortBypassNotDispatched
		}
	}
	params.ReportingTreatmentApplied = &applied
}
