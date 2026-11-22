package candidates_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/bandswap"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/sessionpin"
)

func TestBandSwapTargetServesTheBandsSideOfThePair(t *testing.T) {
	pin := sessionpin.Pin{
		Provider: "anthropic", Model: "claude-opus-4-7",
		PairedProvider: "anthropic", PairedModel: "claude-haiku-4-5", Reason: "cluster",
	}
	large, small := pin.OrderedBandPair()

	assert.Equal(t, large, candidates.BandSwapTarget(pin, bandswap.Large))
	assert.Equal(t, small, candidates.BandSwapTarget(pin, bandswap.Small))
}

func TestBandSwapEligibilityAdmitsOnlyAvailableIncludedEnabledTargets(t *testing.T) {
	served := router.Decision{Provider: "anthropic", Model: "claude-haiku-4-5"}
	base := candidates.BandSwapEligibility{Available: map[string]struct{}{served.Model: {}}}

	assert.True(t, base.Admits(served))

	unavailable := base
	unavailable.Available = map[string]struct{}{}
	assert.False(t, unavailable.Admits(served))

	excluded := base
	excluded.Excluded = map[string]struct{}{served.Model: {}}
	assert.False(t, excluded.Admits(served))

	disabledProvider := base
	disabledProvider.EnabledProviders = map[string]struct{}{"openai": {}}
	assert.False(t, disabledProvider.Admits(served))
}
