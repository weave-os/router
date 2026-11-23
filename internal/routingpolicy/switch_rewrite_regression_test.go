package routingpolicy_test

import (
	"testing"

	"weave-os/router/internal/routingpolicy"

	"github.com/stretchr/testify/assert"
)

func TestDecideSwitchHoldsADowngradeWhoseCacheRewriteOutweighsTenTurnsOfSavings(t *testing.T) {
	verdict := routingpolicy.DecideSwitch(routingpolicy.SwitchInput{
		Current:              "claude-opus-5-5",
		Fresh:                "claude-sonnet-5-5",
		EstimatedInputTokens: 400_000,
	})
	assert.Equal(t, "claude-opus-5-5", verdict.Serve)
	assert.False(t, verdict.Switch)
	assert.Equal(t, routingpolicy.SwitchReason("downgrade_rewrite_exceeds_savings"), verdict.Reason)
}
