package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/router/hmm"
)

func TestHMMConfigFromEnvDefaults(t *testing.T) {
	cfg, err := hmmConfigFromEnv()
	require.NoError(t, err)
	want := hmm.DefaultConfig()
	want.DowngradeHysteresisShadowTurns = defaultHMMDowngradeHysteresisShadowTurns
	assert.Equal(t, want, cfg)
}

func TestHMMConfigFromEnvMapsEveryField(t *testing.T) {
	for key, value := range map[string]string{
		envHMMUpgradeConfidenceThreshold:     "0.2",
		envHMMUpgradeGate:                    "false",
		envHMMUpgradePolicy:                  "off",
		envHMMUpgradeVotes:                   "0",
		envHMMUpgradeSlidingThreshold:        "true",
		envHMMUpgradeShadowMargin:            "0.2",
		envHMMUpgradeShadowStaleAfter:        "2h",
		envHMMDowngradeGate:                  "true",
		envHMMDowngradeHysteresisTurns:       "0",
		envHMMDowngradeHysteresisShadowTurns: "5",
		envHMMAuthorityCacheShadow:           "false",
	} {
		t.Setenv(key, value)
	}
	cfg, err := hmmConfigFromEnv()
	require.NoError(t, err)
	margin := 0.2
	assert.Equal(t, hmm.Config{
		UpgradeConfidenceThreshold:     0.2,
		UpgradePolicy:                  flags.AuthoritativeUpgradePolicyOff,
		UpgradeSlidingThreshold:        true,
		UpgradeShadow:                  hmm.UpgradeShadowConfig{MarginThreshold: &margin, StalePinAfter: 2 * time.Hour},
		DowngradeGate:                  true,
		DowngradeHysteresisShadowTurns: 5,
	}, cfg)
}

func TestHMMConfigFromEnvKeepsThresholdDefaultOutsideUnitInterval(t *testing.T) {
	for _, raw := range []string{"-0.1", "1.5", "x"} {
		t.Setenv(envHMMUpgradeConfidenceThreshold, raw)
		cfg, err := hmmConfigFromEnv()
		require.NoError(t, err)
		assert.InDelta(t, hmm.DefaultUpgradeConfidenceThreshold, cfg.UpgradeConfidenceThreshold, 1e-9, raw)
	}
	t.Setenv(envHMMUpgradeConfidenceThreshold, "0")
	cfg, err := hmmConfigFromEnv()
	require.NoError(t, err)
	assert.InDelta(t, 0.0, cfg.UpgradeConfidenceThreshold, 1e-9)
}

func TestHMMConfigFromEnvRejectsInvalidPolicy(t *testing.T) {
	t.Setenv(envHMMUpgradePolicy, "sometimes")
	_, err := hmmConfigFromEnv()
	require.Error(t, err)
}

func TestHMMConfigFromEnvUpgradeShadow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		margin string
		age    string
		valid  bool
	}{
		{"calibrated", "0.2", "2h", true},
		{"zero margin", "0", "0s", true},
		{"unit margin", "1", "1s", true},
		{"negative margin", "-0.1", "0s", false},
		{"large margin", "1.1", "0s", false},
		{"nan", "NaN", "0s", false},
		{"infinite", "+Inf", "0s", false},
		{"empty margin", "", "0s", false},
		{"trailing junk", "0.2junk", "0s", false},
		{"negative age", "0.2", "-1s", false},
		{"unitless age", "0.2", "10", false},
		{"overflow age", "0.2", "999999999999999999h", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envHMMUpgradeShadowMargin, tc.margin)
			t.Setenv(envHMMUpgradeShadowStaleAfter, tc.age)
			cfg, err := hmmConfigFromEnv()
			if !tc.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, cfg.UpgradeShadow.MarginThreshold)
			if tc.name == "calibrated" {
				assert.InDelta(t, 0.2, *cfg.UpgradeShadow.MarginThreshold, 1e-9)
				assert.Equal(t, 2*time.Hour, cfg.UpgradeShadow.StalePinAfter)
			}
		})
	}
}
