package config_test

import (
	"github.com/stretchr/testify/require"
	"testing"
	"weave-os/router/internal/config"
)

func TestCodexExtraCreditsOptIn(t *testing.T) {
	for _, tc := range []struct {
		raw           string
		want, invalid bool
	}{{"", false, false}, {"false", false, false}, {"true", true, false}, {" TRUE ", true, false}, {"False", false, false}, {"typo", false, true}, {"1", false, true}, {"t", false, true}, {"T", false, true}, {"0", false, true}, {"f", false, true}, {"F", false, true}} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("ROUTER_CODEX_ALLOW_EXTRA_CREDITS", tc.raw)
			got, err := config.CodexExtraCreditsAllowed()
			if tc.invalid {
				require.ErrorContains(t, err, `"`+tc.raw+`"`)
				require.False(t, got)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			}
		})
	}
}
