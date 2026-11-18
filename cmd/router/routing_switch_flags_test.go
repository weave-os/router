package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/router/planner"
)

func TestWithRoutingSwitchDefaultsCarriesHMMPhaseChangeGate(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		defaults := withRoutingSwitchDefaults(map[flags.Key]string{}, false, enabled, false, false, false, planner.EVConfig{})
		assert.Equal(t, boolDefault(enabled), defaults[flags.KeyHMMPhaseChangeGate])
		assert.Equal(t, "false", defaults[flags.KeyHMMSameTierPin])
	}
}
