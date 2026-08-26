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

// initLogger must attach the service tag, or a shared log sink cannot be
// filtered down to this process.
func TestInitLoggerAttachesServiceTag(t *testing.T) {
	t.Setenv("NAME", "router-test-svc")
	t.Setenv("LOG_FORMAT", "json")

	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, nil)).With("name", serviceName())
	logger.Info("hello")

	assert.Contains(t, buf.String(), `"name":"router-test-svc"`)
}
