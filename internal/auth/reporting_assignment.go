package auth

import "context"

// ReportingMode identifies the assignment scheme recorded for reporting.
type ReportingMode string

const (
	ReportingModePercentage ReportingMode = "percentage"
	ReportingModeTeams      ReportingMode = "teams"
)

// ReportingAssignment is observational metadata from the admission snapshot.
// It must not be used to select a routing policy or experiment mode.
type ReportingAssignment struct {
	Mode         ReportingMode
	ExperimentID string
	Revision     int64
	Arm          BlindExperimentArm
	SubjectKey   string
}

// ReportingAssignmentFrom returns only explicitly marked, identified assignments.
// Unmarked producers and identity-less requests have unavailable provenance.
func ReportingAssignmentFrom(ctx context.Context) (ReportingAssignment, bool) {
	policy := RoutingPolicyFrom(ctx)
	if policy.Mode != "" && policy.Mode != RoutingPolicyInherit {
		assigned, confirmed := ctx.Value(routingDecisionContextKey{}).(bool)
		subject := UserIDFrom(ctx)
		if policy.Mode != RoutingPolicyAssigned || policy.ReportingExperimentID == "" || policy.Revision <= 0 || !confirmed || subject == "" {
			return ReportingAssignment{}, false
		}
		arm := BlindExperimentArmPassthrough
		if assigned {
			arm = BlindExperimentArmRouterOn
		}
		return ReportingAssignment{Mode: ReportingModeTeams, ExperimentID: policy.ReportingExperimentID, Revision: policy.Revision, Arm: arm, SubjectKey: subject}, true
	}
	state, active := BlindExperimentFrom(ctx)
	if !active || state.CohortExperimentID != "" || state.ReportingExperimentID == "" || state.ReportingRevision <= 0 || state.CanonicalSubjectKey == "" || !state.Arm.Valid() {
		return ReportingAssignment{}, false
	}
	return ReportingAssignment{Mode: ReportingModePercentage, ExperimentID: state.ReportingExperimentID, Revision: state.ReportingRevision, Arm: state.Arm, SubjectKey: state.CanonicalSubjectKey}, true
}
