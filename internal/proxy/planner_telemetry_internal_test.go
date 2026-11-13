package proxy

import (
	"testing"

	"weave-os/router/internal/router/planner"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyPlannerTelemetry_SkippedWhenPlannerDidNotRun(t *testing.T) {
	p := InsertTelemetryParams{RequestID: "r"}
	applyPlannerTelemetry(&p, turnLoopResult{})
	assert.Empty(t, p.PlannerOutcome)
	assert.Empty(t, p.PlannerReason)
	assert.Nil(t, p.PlannerExpectedSavingsUSD)
	assert.Nil(t, p.PlannerPinCacheCold)
	assert.Empty(t, p.PlannerShadowOutcome)
}

func TestApplyPlannerTelemetry_StayPopulatesPinAndEV(t *testing.T) {
	p := InsertTelemetryParams{}
	applyPlannerTelemetry(&p, turnLoopResult{
		PinModel:    "claude-sonnet-5",
		PinProvider: "anthropic",
		PlannerDecision: planner.Decision{
			Outcome:            planner.OutcomeStay,
			Reason:             planner.ReasonEVNegative,
			ExpectedSavingsUSD: 0.01,
			EvictionCostUSD:    0.04,
			PinCacheCold:       false,
		},
	})
	assert.Equal(t, "stay", p.PlannerOutcome)
	assert.Equal(t, planner.ReasonEVNegative, p.PlannerReason)
	assert.Equal(t, "claude-sonnet-5", p.PlannerPinModel)
	assert.Equal(t, "anthropic", p.PlannerPinProvider)
	require.NotNil(t, p.PlannerExpectedSavingsUSD)
	assert.InDelta(t, 0.01, *p.PlannerExpectedSavingsUSD, 1e-12)
	require.NotNil(t, p.PlannerEvictionCostUSD)
	assert.InDelta(t, 0.04, *p.PlannerEvictionCostUSD, 1e-12)
	require.NotNil(t, p.PlannerPinCacheCold)
	assert.False(t, *p.PlannerPinCacheCold)
	assert.Empty(t, p.PlannerShadowOutcome)
	assert.Nil(t, p.PlannerShadowExpectedSavingsUSD)
}

func TestApplyPlannerTelemetry_SwitchRecordsAbandonedPin(t *testing.T) {
	p := InsertTelemetryParams{}
	applyPlannerTelemetry(&p, turnLoopResult{
		PinModel:    "claude-opus-5",
		PinProvider: "anthropic",
		PlannerDecision: planner.Decision{
			Outcome:                  planner.OutcomeSwitch,
			Reason:                   planner.ReasonEVPositive,
			ExpectedSavingsUSD:       0.12,
			EvictionCostUSD:          0.03,
			PinCacheCold:             true,
			ShadowComputed:           true,
			ShadowOutcome:            planner.OutcomeStay,
			ShadowExpectedSavingsUSD: 0.002,
		},
	})
	assert.Equal(t, "switch", p.PlannerOutcome)
	assert.Equal(t, "claude-opus-5", p.PlannerPinModel)
	require.NotNil(t, p.PlannerPinCacheCold)
	assert.True(t, *p.PlannerPinCacheCold)
	assert.Equal(t, "stay", p.PlannerShadowOutcome)
	require.NotNil(t, p.PlannerShadowExpectedSavingsUSD)
	assert.InDelta(t, 0.002, *p.PlannerShadowExpectedSavingsUSD, 1e-12)
}
