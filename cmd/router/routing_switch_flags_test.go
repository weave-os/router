package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/router/planner"
)

func TestWithRoutingSwitchDefaultsCarriesHMMPhaseChangeGate(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		defaults := withRoutingSwitchDefaults(map[flags.Key]string{}, enabled, false, false, false, planner.EVConfig{})
		assert.Equal(t, boolDefault(enabled), defaults[flags.KeyHMMPhaseChangeGate])
	}
}
