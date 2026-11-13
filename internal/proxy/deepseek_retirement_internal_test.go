package proxy

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeepSeekFamilyAliasesUseV4_1Flash(t *testing.T) {
	for _, alias := range []string{"deepseek", "deepseek-flash", "deepseek-v4-1-flash", "deepseek-v4p1-flash"} {
		t.Run(alias, func(t *testing.T) {
			model, _, known := resolveForceModel(alias)
			require.True(t, known)
			require.Equal(t, "deepseek/deepseek-v4.1-flash", model)
		})
	}
}

func TestDeepSeekExplicitV4PinsKeepTheirIdentity(t *testing.T) {
	for _, alias := range []string{"deepseek-v4-flash", "deepseek/deepseek-v4-flash"} {
		t.Run(alias, func(t *testing.T) {
			model, _, known := resolveForceModel(alias)
			require.True(t, known)
			require.Equal(t, "deepseek/deepseek-v4-flash", model)
		})
	}
}
