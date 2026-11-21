package observability

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ResolveServiceName is what puts the "name" tag on every line. NAME is set per
// service by the deployment; the fallbacks keep output tagged anywhere else.
func TestResolveServiceNamePrefersFirstNonBlankThenDefault(t *testing.T) {
	tests := []struct {
		name       string
		candidates []string
		expects    string
	}{
		{"first wins", []string{"router", "other"}, "router"},
		{"second fallback", []string{"", "router-hmm-sidecar"}, "router-hmm-sidecar"},
		{"default when unset", []string{"", ""}, defaultServiceName},
		{"whitespace-only is not a name", []string{"   ", ""}, defaultServiceName},
		{"value is trimmed", []string{"  svc  ", ""}, "svc"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expects, ResolveServiceName(tc.candidates...))
		})
	}
}

// buildLogger is what initLogger and Configure install, so this fails if the
// service tag is ever dropped from it.
func TestBuildLoggerAttachesServiceTag(t *testing.T) {
	var buf strings.Builder
	buildLogger(slog.NewJSONHandler(&buf, nil), "router-test-svc").Info("hello")
	assert.Contains(t, buf.String(), `"name":"router-test-svc"`)

	buf.Reset()
	buildLogger(slog.NewJSONHandler(&buf, nil), "").Info("hello")
	assert.Contains(t, buf.String(), `"name":"router"`)
}

// ParseLevel gates whether Debug lines survive at all.
func TestParseLevel(t *testing.T) {
	for raw, want := range map[string]slog.Level{
		"debug":     slog.LevelDebug,
		" DEBUG ":   slog.LevelDebug,
		"warn":      slog.LevelWarn,
		"warning":   slog.LevelWarn,
		"error":     slog.LevelError,
		"":          slog.LevelInfo,
		"bogus":     slog.LevelInfo,
		"info":      slog.LevelInfo,
		"\tError\n": slog.LevelError,
	} {
		t.Run(raw, func(t *testing.T) {
			assert.Equal(t, want, ParseLevel(raw))
		})
	}
}

func TestParseFormat(t *testing.T) {
	for raw, want := range map[string]string{
		"json":   "json",
		" JSON ": "json",
		"text":   "text",
		"color":  "tint",
		"tint":   "tint",
		"":       "",
		"pretty": "",
	} {
		t.Run(raw, func(t *testing.T) {
			assert.Equal(t, want, ParseFormat(raw))
		})
	}
}

func TestParseColor(t *testing.T) {
	tests := []struct {
		logColor   string
		noColorSet bool
		want       ColorMode
	}{
		{"1", false, ColorOn},
		{"true", true, ColorOn},
		{" YES ", false, ColorOn},
		{"on", false, ColorOn},
		{"0", false, ColorOff},
		{"false", false, ColorOff},
		{"no", false, ColorOff},
		{"off", false, ColorOff},
		{"", true, ColorOff},
		{"bogus", true, ColorOff},
		{"", false, ColorAuto},
		{"bogus", false, ColorAuto},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, ParseColor(tc.logColor, tc.noColorSet), "LOG_COLOR=%q NO_COLOR set=%t", tc.logColor, tc.noColorSet)
	}
}

func TestUseColorHonorsExplicitModes(t *testing.T) {
	assert.True(t, useColor(ColorOn))
	assert.False(t, useColor(ColorOff))
	assert.Equal(t, isTerminal(), useColor(ColorAuto))
}

func TestConfigureSuppliesTheLazilyInstalledLoggerConfig(t *testing.T) {
	previous := currentConfig()
	t.Cleanup(func() { Configure(previous) })

	Configure(Config{Level: slog.LevelError, ServiceName: "configured", GCPProjectID: "project"})
	assert.Equal(t, Config{Level: slog.LevelError, ServiceName: "configured", GCPProjectID: "project"}, currentConfig())
}
