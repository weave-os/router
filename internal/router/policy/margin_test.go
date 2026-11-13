package policy_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

func TestSidecarRouterPreservesNullableClassifierMargin(t *testing.T) {
	model := catalog.ModelIDGPT55.String()
	zero, margin := 0.0, 0.22
	for _, tc := range []struct {
		name   string
		margin *float64
	}{
		{name: "present", margin: &margin},
		{name: "zero", margin: &zero},
		{name: "absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decider := &recordingPolicy{result: policy.Result{Model: model, Score: 0.7, Margin: tc.margin}}
			adapter := policy.NewSidecarRouter(policy.SidecarRouterConfig{Strategy: router.StrategyHMM}, decider,
				policy.NewResolver(set(model), set(providers.ProviderOpenAI), catalogRosterID, policy.ManagedProviderPolicy()))
			decision, err := adapter.Route(context.Background(), router.Request{})
			require.NoError(t, err)
			require.NotNil(t, decision.Metadata)
			assert.Equal(t, tc.margin, decision.Metadata.ClassifierMargin)
			assert.Equal(t, model, decision.Model)
			assert.Equal(t, float32(0.7), decision.Metadata.ChosenScore)
		})
	}
}
