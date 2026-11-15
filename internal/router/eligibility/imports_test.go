package eligibility_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEligibilityStaysALeafOfRouterAndCatalog(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", ".").Output()
	require.NoError(t, err)
	for _, dep := range strings.Fields(string(out)) {
		require.NotEqual(t, "weave-os/router/internal/router", dep)
		require.NotEqual(t, "weave-os/router/internal/router/catalog", dep)
	}
}
