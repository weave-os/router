package hmm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/sessionpin"
)

var (
	cheapModel  = string(catalog.ModelIDClaudeHaiku45)
	pricyModel  = string(catalog.ModelIDClaudeSonnet46)
	allGuardsOn = hmm.AuthoritativeGuards{
		ConfidenceThreshold: 0.6,
		UpgradeGate:         true,
		DowngradeGate:       true,
	}
)

func guardPin(model string, downgradeVotes int) sessionpin.Pin {
	return sessionpin.Pin{Provider: providers.ProviderAnthropic, Model: model, ConsecutiveDowngradeVotes: downgradeVotes}
}

func scoredPick(model string, score float32) router.Decision {
	return router.Decision{Provider: providers.ProviderAnthropic, Model: model, Metadata: &router.RoutingMetadata{ChosenScore: score}}
}

func TestAuthoritativeGuardsServeWithoutALivePin(t *testing.T) {
	fresh := scoredPick(pricyModel, 0.1)
	assert.Equal(t, hmm.AuthoritativeServe, allGuardsOn.Judge(guardPin(cheapModel, 0), false, fresh, 1000).Hold)
	assert.Equal(t, hmm.AuthoritativeServe, allGuardsOn.Judge(guardPin(pricyModel, 0), true, fresh, 1000).Hold, "same model")
}

func TestAuthoritativeGuardsUpgradeConfidenceFloor(t *testing.T) {
	pin := guardPin(cheapModel, 0)
	got := allGuardsOn.Judge(pin, true, scoredPick(pricyModel, 0.4), 1000)
	assert.Equal(t, hmm.AuthoritativeHoldUpgradeConfidenceLow, got.Hold)
	assert.False(t, got.Downgrade)
	assert.InDelta(t, 0.4, got.Confidence, 1e-6)

	assert.Equal(t, hmm.AuthoritativeServe, allGuardsOn.Judge(pin, true, scoredPick(pricyModel, 0.8), 1000).Hold, "confident")
	assert.Equal(t, hmm.AuthoritativeServe, allGuardsOn.Judge(pin, true, router.Decision{Model: pricyModel}, 1000).Hold, "unscored")

	gateOff := allGuardsOn
	gateOff.UpgradeGate = false
	assert.Equal(t, hmm.AuthoritativeServe, gateOff.Judge(pin, true, scoredPick(pricyModel, 0.4), 1000).Hold)
}

func TestAuthoritativeGuardsUnconfidentDowngradeKeepsVotes(t *testing.T) {
	guards := allGuardsOn
	guards.HysteresisTurns = 3
	got := guards.Judge(guardPin(pricyModel, 1), true, scoredPick(cheapModel, 0.4), 1000)
	assert.Equal(t, hmm.AuthoritativeHoldDowngradeConfidenceLow, got.Hold)
	assert.True(t, got.Downgrade)
	assert.Equal(t, 1, got.DowngradeVotes, "an unconfident vote is not counted")
	assert.Equal(t, 3, got.HysteresisTurns)
}

func TestAuthoritativeGuardsDowngradeHysteresis(t *testing.T) {
	guards := allGuardsOn
	guards.DowngradeGate = false
	guards.HysteresisTurns = 2

	held := guards.Judge(guardPin(pricyModel, 0), true, scoredPick(cheapModel, 0.4), 1000)
	assert.Equal(t, hmm.AuthoritativeHoldDowngradeHysteresis, held.Hold)
	assert.Equal(t, 1, held.DowngradeVotes)

	served := guards.Judge(guardPin(pricyModel, 1), true, scoredPick(cheapModel, 0.4), 1000)
	assert.Equal(t, hmm.AuthoritativeServe, served.Hold)
	assert.Equal(t, 2, served.DowngradeVotes)
}

func TestAuthoritativeGuardsServedDowngradeShadow(t *testing.T) {
	guards := hmm.AuthoritativeGuards{ConfidenceThreshold: 0.6, ShadowHysteresisTurns: 3}
	got := guards.Judge(guardPin(pricyModel, 0), true, scoredPick(cheapModel, 0.4), 1000)
	assert.Equal(t, hmm.AuthoritativeServe, got.Hold)
	assert.True(t, got.Downgrade)
	assert.Equal(t, 3, got.ShadowHysteresisTurns)
	assert.True(t, got.HysteresisWouldHold)
	assert.True(t, got.ConfidenceWouldHold)

	confident := guards.Judge(guardPin(pricyModel, 2), true, scoredPick(cheapModel, 0.9), 1000)
	assert.False(t, confident.HysteresisWouldHold)
	assert.False(t, confident.ConfidenceWouldHold)
}
