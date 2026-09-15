package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"weave-os/router/internal/proxy"
)

const (
	upgradeShadowMarginEnv = "ROUTER_AUTHORITATIVE_UPGRADE_SHADOW_MARGIN"
	upgradeShadowAgeEnv    = "ROUTER_AUTHORITATIVE_UPGRADE_SHADOW_STALE_AFTER"
)

func authoritativeUpgradeConfigFromEnv() (proxy.AuthoritativeUpgradeConfig, error) {
	var calibration proxy.AuthoritativeUpgradeConfig
	if raw, configured := os.LookupEnv(upgradeShadowMarginEnv); configured {
		margin, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return calibration, fmt.Errorf("parse %s: %w", upgradeShadowMarginEnv, err)
		}
		calibration.MarginThreshold = &margin
	}
	if raw, configured := os.LookupEnv(upgradeShadowAgeEnv); configured {
		age, err := time.ParseDuration(raw)
		if err != nil {
			return calibration, fmt.Errorf("parse %s: %w", upgradeShadowAgeEnv, err)
		}
		calibration.StalePinAfter = age
	}
	return calibration, calibration.Validate()
}
