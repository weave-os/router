package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotDoesNotChangePercentageAssignment(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, percent := range []int{0, 30, 100} {
			for _, override := range []BlindExperimentArm{"", BlindExperimentArmRouterOn, BlindExperimentArmPassthrough} {
				record := BlindExperimentRecord{Configured: true, Enabled: enabled, RouterOnPercentage: percent, Seed: "stable", CanonicalSubjectKey: "subject", ManualOverride: override}
				before := resolveBlindExperiment(record, "user")
				record.ExperimentSnapshotID = 7
				after := resolveBlindExperiment(record, "user").AtTime(time.Unix(500, 0))
				if enabled && override == "" {
					require.Equal(t, int64(7), after.ExperimentSnapshotID)
				} else {
					require.Zero(t, after.ExperimentSnapshotID)
				}
				after.ExperimentSnapshotID = 0
				assert.Equal(t, before, after)
			}
		}
	}
}

func TestSnapshotRejectsMaterializedAssignmentMismatch(t *testing.T) {
	record := BlindExperimentRecord{Configured: true, Enabled: true, RouterOnPercentage: 100, Seed: "stable", CanonicalSubjectKey: "subject", AutomaticArm: BlindExperimentArmPassthrough, ExperimentSnapshotID: 7}
	state := resolveBlindExperiment(record, "user")
	assert.Equal(t, BlindExperimentArmPassthrough, state.Arm)
	assert.Zero(t, state.ExperimentSnapshotID)
}

func TestSnapshotDoesNotChangeScheduledCohort(t *testing.T) {
	record := BlindExperimentRecord{Configured: true, Enabled: true, CohortExperimentID: "scheduled", CohortGroupID: 1, CohortRevision: 3}
	before := resolveBlindExperiment(record, "user")
	record.ExperimentSnapshotID = 7
	assert.Equal(t, before, resolveBlindExperiment(record, "user"))
}

func TestTeamSnapshotRequiresConfirmedMatchingAssignment(t *testing.T) {
	for _, mode := range []RoutingPolicyMode{RoutingPolicyAssigned, RoutingPolicyPassthrough, RoutingPolicyInherit} {
		for _, marked := range []bool{false, true} {
			for _, identified := range []bool{false, true} {
				for _, confirmed := range []bool{false, true} {
					for _, assigned := range []bool{false, true} {
						for _, frozenAssigned := range []bool{false, true} {
							policy := RoutingPolicy{Mode: mode, Revision: 4, ExperimentRouterUserIDs: map[string]struct{}{}}
							if marked {
								policy.ExperimentSnapshotID = 9
							}
							if frozenAssigned {
								policy.ExperimentRouterUserIDs["user"] = struct{}{}
							}
							ctx := context.Background()
							if identified {
								ctx = context.WithValue(ctx, UserIDContextKey{}, "user")
							}
							if confirmed {
								ctx = context.WithValue(ctx, routingDecisionContextKey{}, assigned)
							}
							ctx = context.WithValue(ctx, routingPolicyContextKey{}, policy)
							before := RoutingPassthroughFrom(ctx)
							assignment, ok := ReportingAssignmentFrom(ctx)
							require.Equal(t, mode == RoutingPolicyAssigned && marked && identified && confirmed && assigned == frozenAssigned, ok)
							assert.Equal(t, before, RoutingPassthroughFrom(ctx))
							if ok {
								assert.Equal(t, int64(9), assignment.SnapshotID)
							}
						}
					}
				}
			}
		}
	}
}

func TestPercentageSnapshotRequiresAutomaticActiveIdentity(t *testing.T) {
	for _, id := range []int64{0, 9} {
		for _, active := range []bool{false, true} {
			for _, assignmentSource := range []BlindExperimentAssignmentSource{BlindExperimentAssignmentAutomatic, BlindExperimentAssignmentManual} {
				for _, subject := range []string{"", "subject"} {
					state := BlindExperimentState{Active: active, Arm: BlindExperimentArmRouterOn, CanonicalSubjectKey: subject, AssignmentSource: assignmentSource, ExperimentSnapshotID: id}
					assignment, ok := ReportingAssignmentFrom(context.WithValue(context.Background(), BlindExperimentContextKey{}, state))
					require.Equal(t, id > 0 && active && subject != "" && assignmentSource == BlindExperimentAssignmentAutomatic, ok)
					if ok {
						assert.Equal(t, id, assignment.SnapshotID)
					}
				}
			}
		}
	}
}
