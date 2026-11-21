package candidates_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
)

func TestPolicyDeadlineDefault(t *testing.T) {
	const model = "claude-sonnet-4-5"
	served := []string{providers.ProviderAnthropic}
	tests := map[string]struct {
		model     string
		req       router.Request
		served    []string
		want      router.Decision
		wantFound bool
	}{
		"no default model configured": {served: served},
		"request excludes the model":  {model: model, served: served, req: router.Request{Candidates: router.Candidates{ExcludedModels: map[string]struct{}{model: {}}}}},
		"safety excludes the model":   {model: model, served: served, req: router.Request{Candidates: router.Candidates{SafetyExcludedModels: map[string]struct{}{model: {}}}}},
		"provider not enabled":        {model: model, served: served, req: router.Request{EnabledProviders: map[string]struct{}{providers.ProviderFireworks: {}}}},
		"no client serves the model":  {model: model},
		"serves the default model": {model: model, served: served, wantFound: true, want: router.Decision{
			Provider: providers.ProviderAnthropic, Model: model, Reason: candidates.PolicyDeadlineDefaultReason,
		}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			decision, found := candidates.PolicyDeadlineDefault(tc.model, tc.req, tc.served)
			assert.Equal(t, tc.wantFound, found)
			assert.Equal(t, tc.want, decision)
		})
	}
}
