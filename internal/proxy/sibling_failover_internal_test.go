package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func siblingService(keyed ...string) *Service {
	s := &Service{deploymentKeyedProviders: map[string]struct{}{}}
	for _, p := range keyed {
		s.deploymentKeyedProviders[p] = struct{}{}
	}
	return s
}

func overloadedDecision(md *router.RoutingMetadata) router.Decision {
	return router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-opus-5",
		Metadata: md,
	}
}

func TestSiblingFailoverDecision(t *testing.T) {
	ctx := context.Background()

	t.Run("prefers a candidate off the failed provider", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic, providers.ProviderFireworks)
		got, ok := s.siblingFailoverDecision(ctx, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"claude-opus-5", "claude-sonnet-5", "deepseek/deepseek-v4-pro"},
			CandidateProviders: map[string]string{
				"claude-sonnet-5":          providers.ProviderAnthropic,
				"deepseek/deepseek-v4-pro": providers.ProviderFireworks,
			},
		}), 1_000, 0, 0)
		require.True(t, ok)
		assert.Equal(t, "deepseek/deepseek-v4-pro", got.Model)
		assert.Equal(t, providers.ProviderFireworks, got.Provider)
		assert.Equal(t, ReasonSiblingFailover, got.Reason)
	})

	t.Run("falls back to a same-provider candidate when nothing else is keyed", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic)
		got, ok := s.siblingFailoverDecision(ctx, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"claude-sonnet-5", "deepseek/deepseek-v4-pro"},
			CandidateProviders: map[string]string{
				"claude-sonnet-5":          providers.ProviderAnthropic,
				"deepseek/deepseek-v4-pro": providers.ProviderFireworks,
			},
		}), 1_000, 0, 0)
		require.True(t, ok)
		assert.Equal(t, "claude-sonnet-5", got.Model)
		assert.Equal(t, providers.ProviderAnthropic, got.Provider)
	})

	t.Run("uses the pin's runner-up when the pin carries no candidate vector", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic)
		got, ok := s.siblingFailoverDecision(ctx, overloadedDecision(&router.RoutingMetadata{
			PairedModel: "claude-sonnet-5",
		}), 1_000, 0, 0)
		require.True(t, ok)
		assert.Equal(t, "claude-sonnet-5", got.Model)
	})

	t.Run("drops the arm selection so binding resolution re-resolves", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic)
		md := &router.RoutingMetadata{
			CandidateModels:    []string{"claude-sonnet-5"},
			SelectedArmID:      "arm-opus",
			SelectedUpstreamID: "claude-opus-5-20260101",
			BindingIndex:       2,
		}
		got, ok := s.siblingFailoverDecision(ctx, overloadedDecision(md), 1_000, 0, 0)
		require.True(t, ok)
		assert.Empty(t, got.Metadata.SelectedArmID)
		assert.Empty(t, got.Metadata.SelectedUpstreamID)
		assert.Zero(t, got.Metadata.BindingIndex)
		assert.Equal(t, "arm-opus", md.SelectedArmID, "the source decision's metadata is not mutated")
	})

	t.Run("skips candidates whose context window can't hold the turn", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic)
		_, ok := s.siblingFailoverDecision(ctx, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"claude-sonnet-5"},
		}), 1_100_000, 0, 0)
		assert.False(t, ok, "claude-sonnet-5's extended window still can't serve a 1.1M-token turn")
	})

	t.Run("counts the output reserve against the candidate window", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic)
		md := &router.RoutingMetadata{CandidateModels: []string{"claude-sonnet-5"}}
		_, ok := s.siblingFailoverDecision(ctx, overloadedDecision(md), 990_000, 0, 32_000)
		assert.False(t, ok, "990K of history plus a 32K reserve overflows the window")

		_, ok = s.siblingFailoverDecision(ctx, overloadedDecision(md), 990_000, 0, 4_000)
		assert.True(t, ok)
	})

	t.Run("skips the failed model and installation-excluded candidates", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic)
		excluded := context.WithValue(ctx, InstallationExcludedModelsContextKey{}, []string{"claude-sonnet-5"})
		_, ok := s.siblingFailoverDecision(excluded, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"claude-opus-5", "claude-sonnet-5"},
		}), 1_000, 0, 0)
		assert.False(t, ok)
	})

	t.Run("gateway BYOK rescues via a sibling behind a held gateway key", func(t *testing.T) {
		s := &Service{}
		gwCtx := context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
			{
				Provider:     providers.ProviderOpenAIGateway,
				Plaintext:    []byte("pat"),
				ModelAliases: map[string]string{"grok-4.6": "grok-4.6"},
			},
			{
				Provider:     providers.ProviderAnthropicGateway,
				Plaintext:    []byte("pat"),
				ModelAliases: map[string]string{"claude-opus-5": "claude-opus-5"},
			},
		})
		failed := router.Decision{
			Provider: providers.ProviderOpenAIGateway,
			Model:    "grok-4.6",
			Metadata: &router.RoutingMetadata{
				CandidateModels: []string{"grok-4.6", "claude-opus-5"},
			},
		}
		got, ok := s.siblingFailoverDecision(gwCtx, failed, 1_000, 0, 0)
		require.True(t, ok)
		assert.Equal(t, "claude-opus-5", got.Model)
		assert.Equal(t, providers.ProviderAnthropicGateway, got.Provider)
		assert.Equal(t, ReasonSiblingFailover, got.Reason)
		assert.True(t, s.gatewaySiblingAllowed(gwCtx, got))
	})

	t.Run("gateway BYOK never rescues onto a provider without a held gateway key", func(t *testing.T) {
		s := &Service{deploymentKeyedProviders: map[string]struct{}{providers.ProviderAnthropic: {}}}
		gwCtx := context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
			{
				Provider:     providers.ProviderOpenAIGateway,
				Plaintext:    []byte("pat"),
				ModelAliases: map[string]string{"grok-4.6": "grok-4.6"},
			},
		})
		failed := router.Decision{
			Provider: providers.ProviderOpenAIGateway,
			Model:    "grok-4.6",
			Metadata: &router.RoutingMetadata{
				// opus is deployment-keyed on the vendor binding, but the tenant
				// mandated its gateway: no alias, no rescue.
				CandidateModels: []string{"claude-opus-5"},
			},
		}
		_, ok := s.siblingFailoverDecision(gwCtx, failed, 1_000, 0, 0)
		assert.False(t, ok)
		assert.False(t, s.gatewaySiblingAllowed(gwCtx, router.Decision{Provider: providers.ProviderAnthropic}))
	})

	t.Run("gateway BYOK rescues on the same gateway when it aliases a sibling", func(t *testing.T) {
		s := &Service{}
		gwCtx := context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
			{
				Provider:     providers.ProviderOpenAIGateway,
				Plaintext:    []byte("pat"),
				ModelAliases: map[string]string{"grok-4.6": "grok-4.6", "gpt-5": "gpt-5"},
			},
		})
		failed := router.Decision{
			Provider: providers.ProviderOpenAIGateway,
			Model:    "grok-4.6",
			Metadata: &router.RoutingMetadata{
				CandidateModels: []string{"gpt-5"},
			},
		}
		got, ok := s.siblingFailoverDecision(gwCtx, failed, 1_000, 0, 0)
		require.True(t, ok)
		assert.Equal(t, "gpt-5", got.Model)
		assert.Equal(t, providers.ProviderOpenAIGateway, got.Provider)
	})

	t.Run("no metadata and legacy unkeyed deploys have no candidate", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic)
		_, ok := s.siblingFailoverDecision(ctx, overloadedDecision(nil), 1_000, 0, 0)
		assert.False(t, ok)

		legacy := &Service{}
		_, ok = legacy.siblingFailoverDecision(ctx, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"claude-sonnet-5"},
		}), 1_000, 0, 0)
		assert.False(t, ok, "an unset keyed-provider set can't prove a candidate is dispatchable")
	})
}
