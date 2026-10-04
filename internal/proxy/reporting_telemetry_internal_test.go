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
		bypass  auth.CohortBypassReason
	}{
		{name: "router", arm: auth.BlindExperimentArmRouterOn, routed: &turnLoopResult{Decision: router.Decision{Model: "model"}}, applied: true},
		{name: "control", arm: auth.BlindExperimentArmPassthrough, routed: &turnLoopResult{Decision: router.Decision{Model: "model"}, CallerModelPassthrough: true}, applied: true},
		{name: "force", arm: auth.BlindExperimentArmRouterOn, routed: &turnLoopResult{Decision: router.Decision{Model: "model", Reason: translate.ReasonUserForceModel}}, bypass: auth.CohortBypassForceModel},
		{name: "pin", arm: auth.BlindExperimentArmRouterOn, routed: &turnLoopResult{Decision: router.Decision{Model: "model"}, HardPinned: true}, bypass: auth.CohortBypassHardPin},
		{name: "usage", arm: auth.BlindExperimentArmRouterOn, routed: &turnLoopResult{Decision: router.Decision{Model: "model"}, UsageBypass: true}, bypass: auth.CohortBypassUsageBypass},
		{name: "not dispatched", arm: auth.BlindExperimentArmRouterOn, bypass: auth.CohortBypassNotDispatched},
		{name: "router bypass", arm: auth.BlindExperimentArmRouterOn, routed: &turnLoopResult{Decision: router.Decision{Model: "model"}, CallerModelPassthrough: true}, bypass: auth.CohortBypassNotDispatched},
		{name: "control mismatch", arm: auth.BlindExperimentArmPassthrough, routed: &turnLoopResult{Decision: router.Decision{Model: "model"}}, bypass: auth.CohortBypassNotDispatched},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := auth.BlindExperimentState{Active: true, Arm: tc.arm, CanonicalSubjectKey: "subject", ReportingExperimentID: "experiment", ReportingRevision: 3}
			ctx := context.WithValue(context.Background(), auth.BlindExperimentContextKey{}, state)
			var before turnLoopResult
			if tc.routed != nil {
				before = *tc.routed
			}
			params := InsertTelemetryParams{TrainingAllowed: true}
			applyReportingTelemetry(ctx, &params, tc.routed)
			require.NotNil(t, params.ReportingSchemaVersion)
			assert.Equal(t, int16(1), *params.ReportingSchemaVersion)
			require.NotNil(t, params.ReportingTreatmentApplied)
			assert.Equal(t, tc.applied, *params.ReportingTreatmentApplied)
			assert.Equal(t, tc.bypass, params.ReportingBypassReason)
			assert.Equal(t, tc.arm, params.ReportingAssignedArm)
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
	assert.Nil(t, params.ReportingSchemaVersion)
	assert.Nil(t, params.ReportingTreatmentApplied)
	assert.Empty(t, params.ReportingExperimentID)
	assert.Equal(t, auth.BlindExperimentArmPassthrough, params.BlindExperimentArm)
	assert.False(t, params.TrainingAllowed)
}
