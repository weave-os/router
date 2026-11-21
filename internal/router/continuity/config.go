package continuity

import "weave-os/router/internal/router/planner"

const DefaultPlannerThresholdUSD = 0.001

const DefaultPlannerExpectedRemainingTurns = 3

const DefaultPlannerTierUpgradeEnabled = true

const DefaultPlannerCorrectedEconomics = false

func DefaultConfig() Config {
	return Config{
		PlannerEnabled:       true,
		ScoreToolResultTurns: true,
		Planner: planner.EVConfig{
			ThresholdUSD:           DefaultPlannerThresholdUSD,
			ExpectedRemainingTurns: DefaultPlannerExpectedRemainingTurns,
			TierUpgradeEnabled:     DefaultPlannerTierUpgradeEnabled,
			CorrectedEconomics:     DefaultPlannerCorrectedEconomics,
		},
	}
}
