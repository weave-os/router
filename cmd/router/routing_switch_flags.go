package main

import (
	"weave-os/router/internal/flags"
	"weave-os/router/internal/router/planner"
)

func withRoutingSwitchDefaults(defaults map[flags.Key]string, hardPinExplore, ccOrchToolsCrossVendor, escapeNormalize bool, plannerCfg planner.EVConfig) map[flags.Key]string {
	for key, on := range map[flags.Key]bool{
		flags.KeySessionPinEnabled:         true,
		flags.KeyHardPinExplore:            hardPinExplore,
		flags.KeyCCOrchToolsCrossVendor:    ccOrchToolsCrossVendor,
		flags.KeyEscapeNormalize:           escapeNormalize,
		flags.KeyPlannerTierUpgrade:        plannerCfg.TierUpgradeEnabled,
		flags.KeyPlannerCorrectedEconomics: plannerCfg.CorrectedEconomics,
	} {
		defaults[key] = boolDefault(on)
	}
	defaults[flags.KeyTaskArmExperimentTreatmentPct] = "0"
	defaults[flags.KeyTaskArmExperimentSteps] = ""
	defaults[flags.KeyTaskArmExperimentTreatmentCluster] = ""
	return defaults
}

func boolDefault(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
