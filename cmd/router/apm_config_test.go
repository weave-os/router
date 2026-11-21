package main

import (
	"testing"

	"weave-os/router/internal/observability/apm"

	"github.com/stretchr/testify/assert"
)

func TestAPMConfigFromEnv(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want apm.Config
	}{
		{"defaults", nil, apm.Config{DeploymentEnv: "dev"}},
		{"ENV fallback", map[string]string{"ENV": "staging"}, apm.Config{DeploymentEnv: "staging"}},
		{"ROUTER_DEPLOYMENT_ENV wins", map[string]string{"ROUTER_DEPLOYMENT_ENV": "prod", "ENV": "staging"}, apm.Config{DeploymentEnv: "prod"}},
		{"endpoint and insecure", map[string]string{"WV_APM_OTLP_ENDPOINT": "collector:4317", "WV_APM_OTLP_INSECURE": "true"}, apm.Config{Endpoint: "collector:4317", DeploymentEnv: "dev", Insecure: true}},
		{"insecure is exact true only", map[string]string{"WV_APM_OTLP_INSECURE": "TRUE"}, apm.Config{DeploymentEnv: "dev"}},
		{"insecure 1 is false", map[string]string{"WV_APM_OTLP_INSECURE": "1"}, apm.Config{DeploymentEnv: "dev"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"WV_APM_OTLP_ENDPOINT", "ROUTER_DEPLOYMENT_ENV", "ENV", "WV_APM_OTLP_INSECURE"} {
				t.Setenv(k, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			assert.Equal(t, tc.want, apmConfigFromEnv())
		})
	}
}
