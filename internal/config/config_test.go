package config_test

import (
	"os"
	"testing"

	"log/slog"

	"weave-os/router/internal/config"
	"weave-os/router/internal/observability"

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

func TestLoggingDefaultsWhenUnset(t *testing.T) {
	for _, name := range []string{"LOG_LEVEL", "NAME", "OTEL_SERVICE_NAME", "LOG_FORMAT", "LOG_COLOR", "GCP_PROJECT_ID"} {
		t.Setenv(name, "")
	}
	unsetEnv(t, "NO_COLOR")

	assert.Equal(t, observability.Config{
		Level:       slog.LevelInfo,
		ServiceName: "router",
		Color:       observability.ColorAuto,
	}, config.Logging())
}

func TestLoggingParsesEnv(t *testing.T) {
	t.Setenv("LOG_LEVEL", " Warning ")
	t.Setenv("NAME", "  ")
	t.Setenv("OTEL_SERVICE_NAME", " svc ")
	t.Setenv("LOG_FORMAT", "COLOR")
	t.Setenv("LOG_COLOR", "")
	t.Setenv("NO_COLOR", "")
	t.Setenv("GCP_PROJECT_ID", " project ")

	assert.Equal(t, observability.Config{
		Level:        slog.LevelWarn,
		ServiceName:  "svc",
		Format:       "tint",
		Color:        observability.ColorOff,
		GCPProjectID: "project",
	}, config.Logging())
}

func TestLoggingExplicitColorOverridesNoColor(t *testing.T) {
	t.Setenv("LOG_COLOR", "yes")
	t.Setenv("NO_COLOR", "1")
	assert.Equal(t, observability.ColorOn, config.Logging().Color)
}

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	require.NoError(t, os.Unsetenv(name))
}
