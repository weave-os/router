package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

func TestReportingTreatmentObservesOutcomeWithoutChangingDecision(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arm     auth.BlindExperimentArm
		routed  *turnLoopResult
		applied bool
	}{
		{name: "router", arm: auth.BlindExperimentArmRouterOn, routed: &turnLoopResult{Decision: router.Decision{Model: "model"}}, applied: true},
		{name: "control", arm: auth.BlindExperimentArmPassthrough, routed: &turnLoopResult{Decision: router.Decision{Model: "model"}, CallerModelPassthrough: true}, applied: true},
		{name: "force", arm: auth.BlindExperimentArmRouterOn, routed: &turnLoopResult{Decision: router.Decision{Model: "model", Reason: translate.ReasonUserForceModel}}},
		{name: "pin", arm: auth.BlindExperimentArmRouterOn, routed: &turnLoopResult{Decision: router.Decision{Model: "model"}, HardPinned: true}},
		{name: "usage", arm: auth.BlindExperimentArmRouterOn, routed: &turnLoopResult{Decision: router.Decision{Model: "model"}, UsageBypass: true}},
		{name: "not dispatched", arm: auth.BlindExperimentArmRouterOn},
		{name: "router bypass", arm: auth.BlindExperimentArmRouterOn, routed: &turnLoopResult{Decision: router.Decision{Model: "model"}, CallerModelPassthrough: true}},
		{name: "control mismatch", arm: auth.BlindExperimentArmPassthrough, routed: &turnLoopResult{Decision: router.Decision{Model: "model"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := auth.BlindExperimentState{Active: true, Arm: tc.arm, CanonicalSubjectKey: "subject", ExperimentSnapshotID: 3, AssignmentSource: auth.BlindExperimentAssignmentAutomatic}
			ctx := context.WithValue(context.Background(), auth.BlindExperimentContextKey{}, state)
			var before turnLoopResult
			if tc.routed != nil {
				before = *tc.routed
			}
			params := InsertTelemetryParams{TrainingAllowed: true}
			applyReportingTelemetry(ctx, &params, tc.routed)
			if tc.applied {
				require.NotNil(t, params.ExperimentSnapshotID)
				assert.Equal(t, int64(3), *params.ExperimentSnapshotID)
			} else {
				assert.Nil(t, params.ExperimentSnapshotID)
			}
			assert.Empty(t, params.CohortExperimentID)
			assert.True(t, params.TrainingAllowed)
			if tc.routed != nil {
				assert.Equal(t, before, *tc.routed)
			}
		})
	}
}

func TestUnmarkedReportingKeepsExistingTelemetry(t *testing.T) {
	params := InsertTelemetryParams{TrainingAllowed: true}
	ctx := blindExperimentContext(auth.BlindExperimentArmPassthrough)
	applyBlindExperimentTelemetry(ctx, &params, &turnLoopResult{Decision: router.Decision{Model: "model"}, CallerModelPassthrough: true})
	assert.Nil(t, params.ExperimentSnapshotID)
	assert.Equal(t, auth.BlindExperimentArmPassthrough, params.BlindExperimentArm)
	assert.False(t, params.TrainingAllowed)
}
