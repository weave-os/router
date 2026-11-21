package config_test

import (
	"testing"

	"weave-os/router/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseModelIDMap(t *testing.T) {
	got, err := config.ParseModelIDMap("")
	require.NoError(t, err)
	assert.Nil(t, got)

	got, err = config.ParseModelIDMap("  ")
	require.NoError(t, err)
	assert.Nil(t, got)

	got, err = config.ParseModelIDMap("deepseek/deepseek-v4-flash=deepseek-v4-flash,deepseek/deepseek-v4-pro=deepseek-v4-pro,moonshotai/kimi-k2.6=kimi-k2.6,xiaomi/mimo-v2.5-pro=mimo-v2.5-pro,z-ai/glm-5.2=glm-5.2")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"deepseek/deepseek-v4-flash": "deepseek-v4-flash",
		"deepseek/deepseek-v4-pro":   "deepseek-v4-pro",
		"moonshotai/kimi-k2.6":       "kimi-k2.6",
		"xiaomi/mimo-v2.5-pro":       "mimo-v2.5-pro",
		"z-ai/glm-5.2":               "glm-5.2",
	}, got)

	_, err = config.ParseModelIDMap("no-equals")
	require.Error(t, err)

	_, err = config.ParseModelIDMap("=bare")
	require.Error(t, err)

	_, err = config.ParseModelIDMap("a=b,a=c")
	require.Error(t, err)
}

func TestTranslationCompatibilityMode(t *testing.T) {
	t.Setenv("ROUTER_TRANSLATION_COMPATIBILITY_MODE", "")
	mode, err := config.TranslationCompatibilityMode()
	require.NoError(t, err)
	assert.Equal(t, "shadow", mode)

	t.Setenv("ROUTER_TRANSLATION_COMPATIBILITY_MODE", "EnFoRcE")
	mode, err = config.TranslationCompatibilityMode()
	require.NoError(t, err)
	assert.Equal(t, "enforce", mode)

	t.Setenv("ROUTER_TRANSLATION_COMPATIBILITY_MODE", "invalid")
	_, err = config.TranslationCompatibilityMode()
	require.Error(t, err)
}
