package observability

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// serviceName is what puts the "name" tag on every line. NAME is set per
// service by the deployment; the fallbacks keep output tagged anywhere else.
func TestServiceNamePrefersNAMEThenOTELThenDefault(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		expects string
	}{
		{"NAME wins", map[string]string{"NAME": "router", "OTEL_SERVICE_NAME": "other"}, "router"},
		{"OTEL fallback", map[string]string{"OTEL_SERVICE_NAME": "router-hmm-sidecar"}, "router-hmm-sidecar"},
		{"default when unset", nil, defaultServiceName},
		{"whitespace-only is not a name", map[string]string{"NAME": "   "}, defaultServiceName},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NAME", "")
			t.Setenv("OTEL_SERVICE_NAME", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			assert.Equal(t, tc.expects, serviceName())
		})
	}
}

// buildLogger is what initLogger installs, so this fails if the service tag
// is ever dropped from it. Asserting on a hand-built logger instead would
// restate the logic and stay green through that regression.
func TestBuildLoggerAttachesServiceTag(t *testing.T) {
	t.Setenv("NAME", "router-test-svc")

	var buf strings.Builder
	buildLogger(slog.NewJSONHandler(&buf, nil)).Info("hello")

	assert.Contains(t, buf.String(), `"name":"router-test-svc"`)
}

// resolveLevel gates whether Debug lines survive at all.
func TestResolveLevelFromEnv(t *testing.T) {
	for env, want := range map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"":        slog.LevelInfo,
		"bogus":   slog.LevelInfo,
	} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", env)
			assert.Equal(t, want, resolveLevel())
		})
	}
}
