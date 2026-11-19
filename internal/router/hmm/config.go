package hmm

import (
	"fmt"
	"math"
	"time"

	"weave-os/router/internal/flags"
)

type Config struct {
	UpgradeConfidenceThreshold     float64
	UpgradeGate                    bool
	UpgradePolicy                  flags.AuthoritativeUpgradePolicy
	UpgradeVotes                   int
	UpgradeSlidingThreshold        bool
	UpgradeShadow                  UpgradeShadowConfig
	DowngradeGate                  bool
	DowngradeHysteresisTurns       int
	DowngradeHysteresisShadowTurns int
	AuthorityCacheShadow           bool
}

type UpgradeShadowConfig struct {
	MarginThreshold *float64
	StalePinAfter   time.Duration
}

func DefaultConfig() Config {
	return Config{
		UpgradeConfidenceThreshold: DefaultUpgradeConfidenceThreshold,
		UpgradeGate:                true,
		UpgradePolicy:              flags.AuthoritativeUpgradePolicyScore,
		UpgradeVotes:               3,
		DowngradeHysteresisTurns:   DefaultDowngradeHysteresisTurns,
		AuthorityCacheShadow:       true,
	}
}

func (c UpgradeShadowConfig) Validate() error {
	if c.MarginThreshold != nil && (math.IsNaN(*c.MarginThreshold) || math.IsInf(*c.MarginThreshold, 0) || *c.MarginThreshold < 0 || *c.MarginThreshold > 1) {
		return fmt.Errorf("upgrade evidence margin threshold must be finite and within [0, 1]")
	}
	if c.StalePinAfter < 0 {
		return fmt.Errorf("upgrade evidence stale-pin age must be nonnegative")
	}
	return nil
}
