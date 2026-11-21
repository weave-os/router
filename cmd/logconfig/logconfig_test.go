package logconfig_test

import (
	"log/slog"
	"os"
	"testing"

	"weave-os/router/cmd/logconfig"
	"weave-os/router/internal/observability"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFromEnvDefaultsWhenUnset(t *testing.T) {
	for _, name := range []string{"LOG_LEVEL", "NAME", "OTEL_SERVICE_NAME", "LOG_FORMAT", "LOG_COLOR", "GCP_PROJECT_ID"} {
		t.Setenv(name, "")
	}
	unsetEnv(t, "NO_COLOR")

	assert.Equal(t, observability.Config{
		Level:       slog.LevelInfo,
		ServiceName: "router",
		Color:       observability.ColorAuto,
	}, logconfig.FromEnv())
}

func TestFromEnvParsesEnv(t *testing.T) {
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
	}, logconfig.FromEnv())
}

func TestFromEnvExplicitColorOverridesNoColor(t *testing.T) {
	t.Setenv("LOG_COLOR", "yes")
	t.Setenv("NO_COLOR", "1")
	assert.Equal(t, observability.ColorOn, logconfig.FromEnv().Color)
}

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	require.NoError(t, os.Unsetenv(name))
}
