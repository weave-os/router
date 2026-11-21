package continuity_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/hmm"
)

func TestStrategyPortsValidateNamesEveryMissingPort(t *testing.T) {
	require.EqualError(t, continuity.StrategyPorts{}.Validate(), "strategy ports not wired: Ownership, Facts, Upgrade, CostGate")

	partial := hmm.ServingPorts()
	partial.Upgrade = nil
	require.EqualError(t, partial.Validate(), "strategy ports not wired: Upgrade")
}

func TestStrategyPortsValidateAcceptsHMMServingPorts(t *testing.T) {
	require.NoError(t, hmm.ServingPorts().Validate())
}
