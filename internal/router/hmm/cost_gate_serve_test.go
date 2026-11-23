package hmm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/planner"
)

func TestHMMCostGateServeWithoutStayPinServesFreshUnpinned(t *testing.T) {
	fresh := router.Decision{Provider: providers.ProviderAnthropic, Model: otherModel}
	served := hmmGate().Serve(costGateServeInput(fresh), "")
	assert.Equal(t, fresh, served.Decision)
	assert.False(t, served.Sticky)
	assert.Empty(t, served.StayModel)
	assert.Equal(t, planner.ReasonNoPin, served.Planner.Reason)
	assert.Equal(t, "hmm_fresh_unpinned", served.PinTier)
	assert.Empty(t, served.HeldEffort)
}

func TestHMMCostGateServeHoldsIncumbentEffortAndKeepsTierReason(t *testing.T) {
	fresh := hysteresisDecision(map[string]float32{
		"anthropic/claude-opus-5:low":  4.0,
		"anthropic/claude-opus-5:high": 4.5,
	})
	fresh.Effort = "high"
	served := hmmGate().Serve(costGateServeInput(fresh), "claude-opus-5:low")
	assert.Equal(t, "low", served.Decision.Effort)
	assert.Equal(t, "high", served.ChallengerEffort)
	assert.Equal(t, "low", served.HeldEffort)
	assert.Equal(t, planner.ReasonNoPin+"+effort_hysteresis", served.Planner.Reason)
	assert.Equal(t, "hmm_fresh_unpinned", served.PinTier)
}

func costGateServeInput(fresh router.Decision) hmm.CostGateInput {
	return hmm.CostGateInput{Fresh: fresh, EstimatedInputTokens: 1000, Now: gateNow}
}
