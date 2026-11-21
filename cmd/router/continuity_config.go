package main

import (
	"weave-os/router/internal/config"
	"weave-os/router/internal/router/continuity"
)

const (
	envPlannerEnabled               = "ROUTER_PLANNER_ENABLED"
	envScoreToolResultTurns         = "ROUTER_SCORE_TOOL_RESULT_TURNS"
	envSwitchEVThresholdUSD         = "ROUTER_SWITCH_EV_THRESHOLD_USD"
	envSwitchExpectedRemainingTurns = "ROUTER_SWITCH_EXPECTED_REMAINING_TURNS"
	envSwitchTierUpgradeEnabled     = "ROUTER_SWITCH_TIER_UPGRADE_ENABLED"
	envSwitchCorrectedEconomics     = "ROUTER_SWITCH_CORRECTED_ECONOMICS"
)

func continuityConfigFromEnv() continuity.Config {
	cfg := continuity.DefaultConfig()
	cfg.PlannerEnabled = config.GetOr(envPlannerEnabled, boolDefault(cfg.PlannerEnabled)) == "true"
	cfg.ScoreToolResultTurns = config.GetOr(envScoreToolResultTurns, boolDefault(cfg.ScoreToolResultTurns)) == "true"
	cfg.Planner.ThresholdUSD = parseEnvFloat(envSwitchEVThresholdUSD, cfg.Planner.ThresholdUSD)
	cfg.Planner.ExpectedRemainingTurns = parseEnvInt(envSwitchExpectedRemainingTurns, cfg.Planner.ExpectedRemainingTurns)
	cfg.Planner.TierUpgradeEnabled = config.GetOr(envSwitchTierUpgradeEnabled, boolDefault(cfg.Planner.TierUpgradeEnabled)) == "true"
	cfg.Planner.CorrectedEconomics = config.GetOr(envSwitchCorrectedEconomics, boolDefault(cfg.Planner.CorrectedEconomics)) == "true"
	return cfg
}
