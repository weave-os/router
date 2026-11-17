package main

import (
	"weave-os/router/internal/flags"
	"weave-os/router/internal/router/planner"
)

func withRoutingSwitchDefaults(defaults map[flags.Key]string, hmmSameTierPin, hardPinExplore, ccOrchToolsCrossVendor, escapeNormalize bool, plannerCfg planner.EVConfig) map[flags.Key]string {
	defaults[flags.KeySessionPinEnabled] = boolDefault(true)
	defaults[flags.KeyHMMSameTierPin] = boolDefault(hmmSameTierPin)
	defaults[flags.KeyHardPinExplore] = boolDefault(hardPinExplore)
	defaults[flags.KeyCCOrchToolsCrossVendor] = boolDefault(ccOrchToolsCrossVendor)
	defaults[flags.KeyEscapeNormalize] = boolDefault(escapeNormalize)
	defaults[flags.KeyPlannerTierUpgrade] = boolDefault(plannerCfg.TierUpgradeEnabled)
	defaults[flags.KeyPlannerColdPinFollowFresh] = boolDefault(plannerCfg.ColdPinFollowFresh)
	defaults[flags.KeyPlannerCorrectedEconomics] = boolDefault(plannerCfg.CorrectedEconomics)
	return defaults
}
