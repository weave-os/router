package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"weave-os/router/internal/config"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/router/hmm"
)

const (
	envHMMUpgradeConfidenceThreshold     = "ROUTER_HMM_UPGRADE_CONFIDENCE_THRESHOLD"
	envHMMUpgradeGate                    = "ROUTER_HMM_UPGRADE_GATE"
	envHMMUpgradePolicy                  = "ROUTER_HMM_UPGRADE_POLICY"
	envHMMUpgradeVotes                   = "ROUTER_HMM_UPGRADE_VOTES"
	envHMMUpgradeSlidingThreshold        = "ROUTER_HMM_UPGRADE_SLIDING_THRESHOLD"
	envHMMUpgradeShadowMargin            = "ROUTER_HMM_UPGRADE_SHADOW_MARGIN"
	envHMMUpgradeShadowStaleAfter        = "ROUTER_HMM_UPGRADE_SHADOW_STALE_AFTER"
	envHMMDowngradeGate                  = "ROUTER_HMM_DOWNGRADE_GATE"
	envHMMDowngradeHysteresisTurns       = "ROUTER_HMM_DOWNGRADE_HYSTERESIS_TURNS"
	envHMMDowngradeHysteresisShadowTurns = "ROUTER_HMM_DOWNGRADE_HYSTERESIS_SHADOW_TURNS"
	envHMMAuthorityCacheShadow           = "ROUTER_HMM_AUTHORITY_CACHE_SHADOW"

	defaultHMMDowngradeHysteresisShadowTurns = 2
)

func hmmConfigFromEnv() (hmm.Config, error) {
	cfg := hmm.DefaultConfig()
	threshold := parseEnvFloat(envHMMUpgradeConfidenceThreshold, cfg.UpgradeConfidenceThreshold)
	if threshold >= 0 && threshold <= 1 {
		cfg.UpgradeConfidenceThreshold = threshold
	}
	policy, err := flags.ParseAuthoritativeUpgradePolicy(config.GetOr(envHMMUpgradePolicy, string(cfg.UpgradePolicy)))
	if err != nil {
		return cfg, fmt.Errorf("parse %s: %w", envHMMUpgradePolicy, err)
	}
	cfg.UpgradePolicy = policy
	cfg.UpgradeGate = config.GetOr(envHMMUpgradeGate, boolDefault(cfg.UpgradeGate)) == "true"
	cfg.UpgradeVotes = parseEnvNonNegativeInt(envHMMUpgradeVotes, cfg.UpgradeVotes)
	cfg.UpgradeSlidingThreshold = config.GetOr(envHMMUpgradeSlidingThreshold, boolDefault(cfg.UpgradeSlidingThreshold)) == "true"
	cfg.DowngradeGate = config.GetOr(envHMMDowngradeGate, boolDefault(cfg.DowngradeGate)) == "true"
	cfg.DowngradeHysteresisTurns = parseEnvNonNegativeInt(envHMMDowngradeHysteresisTurns, cfg.DowngradeHysteresisTurns)
	cfg.DowngradeHysteresisShadowTurns = parseEnvNonNegativeInt(envHMMDowngradeHysteresisShadowTurns, defaultHMMDowngradeHysteresisShadowTurns)
	cfg.AuthorityCacheShadow = config.GetOr(envHMMAuthorityCacheShadow, boolDefault(cfg.AuthorityCacheShadow)) == "true"
	if cfg.UpgradeShadow, err = upgradeShadowConfigFromEnv(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func upgradeShadowConfigFromEnv() (hmm.UpgradeShadowConfig, error) {
	var shadow hmm.UpgradeShadowConfig
	if raw, configured := os.LookupEnv(envHMMUpgradeShadowMargin); configured {
		margin, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return shadow, fmt.Errorf("parse %s: %w", envHMMUpgradeShadowMargin, err)
		}
		shadow.MarginThreshold = &margin
	}
	if raw, configured := os.LookupEnv(envHMMUpgradeShadowStaleAfter); configured {
		age, err := time.ParseDuration(raw)
		if err != nil {
			return shadow, fmt.Errorf("parse %s: %w", envHMMUpgradeShadowStaleAfter, err)
		}
		shadow.StalePinAfter = age
	}
	return shadow, shadow.Validate()
}
