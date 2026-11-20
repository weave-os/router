package pinstate_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/hmm/pinstate"
	"weave-os/router/internal/router/sessionpin"
)

func TestColumnsRoundTripHMMPinFields(t *testing.T) {
	columns := sessionpin.StrategyColumns{PolicyGroup: "g2", ConsecutiveDowngradeVotes: 1, ConsecutiveUpgradeVotes: 2}
	pin := pinstate.Accessor{}.WithColumns(sessionpin.Pin{Model: "m"}, columns)
	assert.Equal(t, sessionpin.Pin{Model: "m", PolicyGroup: "g2", ConsecutiveDowngradeVotes: 1, ConsecutiveUpgradeVotes: 2}, pin)
	assert.Equal(t, columns, pinstate.Accessor{}.Columns(pin))
}

func TestWithZeroColumnsClearsHMMPinFields(t *testing.T) {
	pin := sessionpin.Pin{Model: "m", PolicyGroup: "g2", ConsecutiveDowngradeVotes: 1, ConsecutiveUpgradeVotes: 2}
	assert.Equal(t, sessionpin.Pin{Model: "m"}, pinstate.WithColumns(pin, sessionpin.StrategyColumns{}))
}

func TestPinStateStaysALeafSoRoutingPolicyCanImportIt(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", ".").Output()
	require.NoError(t, err)
	for _, dep := range strings.Fields(string(out)) {
		require.NotEqual(t, "weave-os/router/internal/router/hmm", dep)
		require.NotEqual(t, "weave-os/router/internal/routingpolicy", dep)
	}
}
