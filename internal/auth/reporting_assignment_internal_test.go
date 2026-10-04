package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReportingMetadataDoesNotChangePercentageAssignment(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, percent := range []int{0, 30, 100} {
			for _, override := range []BlindExperimentArm{"", BlindExperimentArmRouterOn, BlindExperimentArmPassthrough} {
				record := BlindExperimentRecord{Configured: true, Enabled: enabled, RouterOnPercentage: percent, Seed: "stable", CanonicalSubjectKey: "subject", ManualOverride: override}
				before := resolveBlindExperiment(record, "user")
				record.ReportingExperimentID = "d630b9a0-d7ca-4c1c-978c-069dbe87bc94"
				record.ReportingRevision = 7
				after := resolveBlindExperiment(record, "user").AtTime(time.Unix(500, 0))
				if enabled {
					require.Equal(t, record.ReportingExperimentID, after.ReportingExperimentID)
				}
				after.ReportingExperimentID = ""
				after.ReportingRevision = 0
				assert.Equal(t, before, after, "enabled=%t percent=%d override=%s", enabled, percent, override)
				assert.Empty(t, after.CohortExperimentID)
			}
		}
	}
}

func TestReportingMetadataDoesNotChangeScheduledCohort(t *testing.T) {
	record := BlindExperimentRecord{Configured: true, Enabled: true, CohortExperimentID: "scheduled", CohortGroupID: 1, CohortRevision: 3}
	before := resolveBlindExperiment(record, "user")
	record.ReportingExperimentID = "legacy-marker"
	record.ReportingRevision = 9
	assert.Equal(t, before, resolveBlindExperiment(record, "user"))
}

func TestReportingTeamAssignmentRequiresMarkedConfirmedIdentity(t *testing.T) {
	for _, mode := range []RoutingPolicyMode{RoutingPolicyAssigned, RoutingPolicyPassthrough, RoutingPolicyInherit} {
		for _, marked := range []bool{false, true} {
			for _, identified := range []bool{false, true} {
				for _, confirmed := range []bool{false, true} {
					for _, assigned := range []bool{false, true} {
						policy := RoutingPolicy{Mode: mode, Revision: 4}
						ctx := context.Background()
						if identified {
							ctx = context.WithValue(ctx, UserIDContextKey{}, "user")
						}
						if confirmed {
							ctx = context.WithValue(ctx, routingDecisionContextKey{}, assigned)
						}
						before := RoutingPassthroughFrom(context.WithValue(ctx, routingPolicyContextKey{}, policy))
						if marked {
							policy.ReportingExperimentID = "experiment"
						}
						ctx = context.WithValue(ctx, routingPolicyContextKey{}, policy)
						actual, ok := ReportingAssignmentFrom(ctx)
						assert.Equal(t, before, RoutingPassthroughFrom(ctx))
						want := mode == RoutingPolicyAssigned && marked && identified && confirmed
						require.Equal(t, want, ok)
						if want {
							arm := BlindExperimentArmPassthrough
							if assigned {
								arm = BlindExperimentArmRouterOn
							}
							assert.Equal(t, ReportingAssignment{Mode: "teams", ExperimentID: "experiment", Revision: 4, Arm: arm, SubjectKey: "user"}, actual)
						}
					}
				}
			}
		}
	}
}

func TestReportingPercentageMixedVersionAndInvalidMarkers(t *testing.T) {
	for _, tc := range []struct {
		id       string
		revision int64
		active   bool
		want     bool
	}{
		{"", 0, true, false}, {"experiment", 0, true, false}, {"experiment", 2, false, false}, {"experiment", 2, true, true},
	} {
		state := BlindExperimentState{Active: tc.active, Arm: BlindExperimentArmRouterOn, CanonicalSubjectKey: "subject", ReportingExperimentID: tc.id, ReportingRevision: tc.revision}
		ctx := context.WithValue(context.Background(), BlindExperimentContextKey{}, state)
		actual, ok := ReportingAssignmentFrom(ctx)
		require.Equal(t, tc.want, ok)
		if ok {
			assert.Equal(t, ReportingMode("percentage"), actual.Mode)
			assert.Equal(t, tc.revision, actual.Revision)
		}
	}
}
