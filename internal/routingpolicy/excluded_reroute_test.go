package routingpolicy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/routingpolicy"
)

func TestExcludedRerouteServable(t *testing.T) {
	rerouted := router.Decision{Model: "claude-sonnet-4-5", Provider: providers.ProviderAnthropic}
	tests := map[string]struct {
		req       router.Request
		available map[string]struct{}
		want      bool
	}{
		"no deployment model list":    {want: true},
		"deployment serves the model": {available: map[string]struct{}{rerouted.Model: {}}, want: true},
		"deployment lacks the model":  {available: map[string]struct{}{"claude-haiku-4-5": {}}, want: false},
		"request excludes the model":  {req: router.Request{Candidates: router.Candidates{ExcludedModels: map[string]struct{}{rerouted.Model: {}}}}, want: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, routingpolicy.ExcludedRerouteServable(rerouted, tc.req, tc.available))
		})
	}
}
