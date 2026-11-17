package main

import (
	"weave-os/router/internal/flags"
	"weave-os/router/internal/router/planner"
)

func withRoutingSwitchDefaults(defaults map[flags.Key]string, hmmSameTierPin, hardPinExplore, ccOrchToolsCrossVendor, escapeNormalize bool, plannerCfg planner.EVConfig) map[flags.Key]string {
	for key, on := range map[flags.Key]bool{
		flags.KeySessionPinEnabled:         true,
		flags.KeyHMMSameTierPin:            hmmSameTierPin,
		flags.KeyHardPinExplore:            hardPinExplore,
		flags.KeyCCOrchToolsCrossVendor:    ccOrchToolsCrossVendor,
		flags.KeyEscapeNormalize:           escapeNormalize,
		flags.KeyPlannerTierUpgrade:        plannerCfg.TierUpgradeEnabled,
		flags.KeyPlannerColdPinFollowFresh: plannerCfg.ColdPinFollowFresh,
		flags.KeyPlannerCorrectedEconomics: plannerCfg.CorrectedEconomics,
	} {
		defaults[key] = boolDefault(on)
	}
	return defaults
}
