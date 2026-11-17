package policy_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

func TestSidecarRouterReportsRequestBodySizeWhenNoBindingCanServe(t *testing.T) {
	model := "claude-sonnet-4-6"
	resolver := policy.NewResolver(
		set(model),
		set(providers.ProviderAnthropic),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)
	adapter := policy.NewSidecarRouter(policy.SidecarRouterConfig{Strategy: router.StrategyHMM}, &recordingPolicy{}, resolver)
	oversized := router.Request{
		EnabledProviders: set(providers.ProviderAnthropic),
		RequestBodyBytes: 40 * 1024 * 1024,
	}

	_, err := adapter.Route(context.Background(), oversized)
	require.ErrorIs(t, err, policy.ErrRequestBodyTooLarge)
	require.ErrorIs(t, err, policy.ErrNoRoutableModels)

	resolved := resolver.Resolve(oversized)
	assert.Empty(t, resolved.Candidates)
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: model,
		RosterID:  model,
		Reason:    policy.ExclusionRequestBodySize,
	})

	withinLimit := oversized
	withinLimit.RequestBodyBytes = 1024
	assert.NotEmpty(t, resolver.Resolve(withinLimit).Candidates)
}
