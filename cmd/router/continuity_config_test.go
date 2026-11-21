package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/planner"
)

func TestContinuityConfigFromEnvDefaults(t *testing.T) {
	assert.Equal(t, continuity.DefaultConfig(), continuityConfigFromEnv())
}

func TestContinuityConfigFromEnvMapsEveryField(t *testing.T) {
	for key, value := range map[string]string{
		envPlannerEnabled:               "false",
		envScoreToolResultTurns:         "false",
		envSwitchEVThresholdUSD:         "-0.5",
		envSwitchExpectedRemainingTurns: "7",
		envSwitchTierUpgradeEnabled:     "false",
		envSwitchCorrectedEconomics:     "true",
	} {
		t.Setenv(key, value)
	}
	assert.Equal(t, continuity.Config{
		Planner: planner.EVConfig{ThresholdUSD: -0.5, ExpectedRemainingTurns: 7, CorrectedEconomics: true},
	}, continuityConfigFromEnv())
}

func TestContinuityConfigFromEnvKeepsExpectedTurnsDefaultBelowOne(t *testing.T) {
	for _, raw := range []string{"0", "-2", "x"} {
		t.Setenv(envSwitchExpectedRemainingTurns, raw)
		assert.Equal(t, continuity.DefaultPlannerExpectedRemainingTurns, continuityConfigFromEnv().Planner.ExpectedRemainingTurns, raw)
	}
}
