package auth

import "context"

// ReportingAssignment is observational; it never selects a routing policy.
type ReportingAssignment struct {
	SnapshotID int64
	Arm        BlindExperimentArm
}

func ReportingAssignmentFrom(ctx context.Context) (ReportingAssignment, bool) {
	policy := RoutingPolicyFrom(ctx)
	if policy.Mode != "" && policy.Mode != RoutingPolicyInherit {
		assigned, confirmed := ctx.Value(routingDecisionContextKey{}).(bool)
		subject := UserIDFrom(ctx)
		_, snapshotAssigned := policy.ExperimentRouterUserIDs[subject]
		if policy.Mode != RoutingPolicyAssigned || policy.ExperimentSnapshotID <= 0 || !confirmed || subject == "" || assigned != snapshotAssigned {
			return ReportingAssignment{}, false
		}
		arm := BlindExperimentArmPassthrough
		if assigned {
			arm = BlindExperimentArmRouterOn
		}
		return ReportingAssignment{SnapshotID: policy.ExperimentSnapshotID, Arm: arm}, true
	}
	state, active := BlindExperimentFrom(ctx)
	if !active || state.CohortExperimentID != "" || state.ExperimentSnapshotID <= 0 || state.CanonicalSubjectKey == "" || !state.Arm.Valid() || state.AssignmentSource != BlindExperimentAssignmentAutomatic {
		return ReportingAssignment{}, false
	}
	return ReportingAssignment{SnapshotID: state.ExperimentSnapshotID, Arm: state.Arm}, true
}
