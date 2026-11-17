package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/router/planner"
)

func TestPlannerConfigForLayersRequestFlagsOverDeploymentConfig(t *testing.T) {
	deployment := planner.EVConfig{ThresholdUSD: 0.5, TierUpgradeEnabled: true}
	ctx := flags.WithOverrides(context.Background(), flags.Overrides{Bools: map[flags.Key]bool{
		flags.KeyPlannerTierUpgrade:        false,
		flags.KeyPlannerColdPinFollowFresh: true,
		flags.KeyPlannerCorrectedEconomics: true,
	}})

	assert.Equal(t, deployment, plannerConfigFor(context.Background(), deployment))
	assert.Equal(t, planner.EVConfig{
		ThresholdUSD:       0.5,
		ColdPinFollowFresh: true,
		CorrectedEconomics: true,
	}, plannerConfigFor(ctx, deployment))
}

func TestStripsThinkingHistoryWhenSessionPinsAreDisabled(t *testing.T) {
	disabled := flags.WithOverrides(context.Background(), flags.Overrides{Bools: map[flags.Key]bool{flags.KeySessionPinEnabled: false}})

	assert.False(t, stripsThinkingHistory(context.Background()))
	assert.True(t, stripsThinkingHistory(disabled))
}
