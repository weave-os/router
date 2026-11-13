package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthoritativeUpgradeConfigFromEnv(t *testing.T) {
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
			t.Setenv(upgradeShadowMarginEnv, tc.margin)
			t.Setenv(upgradeShadowAgeEnv, tc.age)
			calibration, err := authoritativeUpgradeConfigFromEnv()
			if !tc.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, calibration.MarginThreshold)
			if tc.name == "calibrated" {
				assert.Equal(t, 0.2, *calibration.MarginThreshold)
				assert.Equal(t, 2*time.Hour, calibration.StalePinAfter)
			}
		})
	}
}
