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

// firstSibling is the head of the rescue walk: the candidate the turn tries first.
func firstSibling(s *Service, ctx context.Context, failed router.Decision, est, sigSavings, outputReserve int) (router.Decision, bool) {
	decisions := s.siblingFailoverDecisions(ctx, failed, est, sigSavings, outputReserve)
	if len(decisions) == 0 {
		return router.Decision{}, false
	}
	return decisions[0], true
}

func siblingModels(decisions []router.Decision) []string {
	models := make([]string, 0, len(decisions))
	for _, d := range decisions {
		models = append(models, d.Model)
	}
	return models
}

func TestSiblingFailoverDecision(t *testing.T) {
	ctx := context.Background()

	t.Run("prefers a candidate off the failed provider", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic, providers.ProviderFireworks)
		got, ok := firstSibling(s, ctx, overloadedDecision(&router.RoutingMetadata{
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

	t.Run("walks the ranked group fallback before the rest of the scored pool", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic, providers.ProviderOpenAI)
		failed := router.Decision{
			Provider: providers.ProviderOpenAI,
			Model:    "gpt-6-astra",
			Metadata: &router.RoutingMetadata{
				// Catalog order puts haiku first; the roster's fallback is opus.
				CandidateModels: []string{"claude-haiku-4-5", "claude-opus-5", "gpt-6-astra", "gpt-5"},
				RescueModels:    []string{"gpt-6-astra", "claude-opus-5", "gpt-5"},
				CandidateProviders: map[string]string{
					"claude-haiku-4-5": providers.ProviderAnthropic,
					"claude-opus-5":    providers.ProviderAnthropic,
					"gpt-5":            providers.ProviderOpenAI,
				},
			},
		}
		got := s.siblingFailoverDecisions(ctx, failed, 1_000, 0, 0)
		assert.Equal(t, []string{"claude-opus-5", "claude-haiku-4-5", "gpt-5"}, siblingModels(got),
			"ranked fallback first, then the pool, with same-provider candidates last")
		for _, d := range got {
			assert.Equal(t, ReasonSiblingFailover, d.Reason)
		}
	})

	t.Run("returns every eligible candidate so a failed rescuer hands off to the next", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic, providers.ProviderFireworks)
		got := s.siblingFailoverDecisions(ctx, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"claude-opus-5", "claude-sonnet-5", "deepseek/deepseek-v4-pro"},
			CandidateProviders: map[string]string{
				"claude-sonnet-5":          providers.ProviderAnthropic,
				"deepseek/deepseek-v4-pro": providers.ProviderFireworks,
			},
			PairedModel: "claude-sonnet-5",
		}), 1_000, 0, 0)
		assert.Equal(t, []string{"deepseek/deepseek-v4-pro", "claude-sonnet-5"}, siblingModels(got),
			"the failed model is dropped and the paired-model duplicate collapses")
	})

	t.Run("falls back to a same-provider candidate when nothing else is keyed", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic)
		got, ok := firstSibling(s, ctx, overloadedDecision(&router.RoutingMetadata{
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
		got, ok := firstSibling(s, ctx, overloadedDecision(&router.RoutingMetadata{
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
		got, ok := firstSibling(s, ctx, overloadedDecision(md), 1_000, 0, 0)
		require.True(t, ok)
		assert.Empty(t, got.Metadata.SelectedArmID)
		assert.Empty(t, got.Metadata.SelectedUpstreamID)
		assert.Zero(t, got.Metadata.BindingIndex)
		assert.Equal(t, "arm-opus", md.SelectedArmID, "the source decision's metadata is not mutated")
	})

	t.Run("skips candidates whose context window can't hold the turn", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic)
		_, ok := firstSibling(s, ctx, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"claude-sonnet-5"},
		}), 1_100_000, 0, 0)
		assert.False(t, ok, "claude-sonnet-5's extended window still can't serve a 1.1M-token turn")
	})

	t.Run("counts the output reserve against the candidate window", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic)
		md := &router.RoutingMetadata{CandidateModels: []string{"claude-sonnet-5"}}
		_, ok := firstSibling(s, ctx, overloadedDecision(md), 990_000, 0, 32_000)
		assert.False(t, ok, "990K of history plus a 32K reserve overflows the window")

		_, ok = firstSibling(s, ctx, overloadedDecision(md), 990_000, 0, 4_000)
		assert.True(t, ok)
	})

	t.Run("skips the failed model and installation-excluded candidates", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic)
		excluded := context.WithValue(ctx, InstallationExcludedModelsContextKey{}, []string{"claude-sonnet-5"})
		_, ok := firstSibling(s, excluded, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"claude-opus-5", "claude-sonnet-5"},
		}), 1_000, 0, 0)
		assert.False(t, ok)
	})

	t.Run("skips a model this session demoted after a committed stream failure", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic, providers.ProviderFireworks)
		md := &router.RoutingMetadata{
			CandidateModels: []string{"claude-opus-5", "claude-sonnet-5", "deepseek/deepseek-v4-pro"},
			CandidateProviders: map[string]string{
				"claude-sonnet-5":          providers.ProviderAnthropic,
				"deepseek/deepseek-v4-pro": providers.ProviderFireworks,
			},
		}
		demoted := context.WithValue(ctx, SessionDemotedModelsContextKey{}, []string{"deepseek/deepseek-v4-pro"})
		got, ok := firstSibling(s, demoted, overloadedDecision(md), 1_000, 0, 0)
		require.True(t, ok)
		assert.Equal(t, "claude-sonnet-5", got.Model, "the demoted arm is skipped even though it ranks first")
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
		got, ok := firstSibling(s, gwCtx, failed, 1_000, 0, 0)
		require.True(t, ok)
		assert.Equal(t, "claude-opus-5", got.Model)
		assert.Equal(t, providers.ProviderAnthropicGateway, got.Provider)
		assert.Equal(t, ReasonSiblingFailover, got.Reason)
		assert.True(t, s.gatewaySiblingAllowed(gwCtx, got))
	})

	t.Run("gateway BYOK walks the ranked fallback before other gateway aliases", func(t *testing.T) {
		s := &Service{}
		gwCtx := context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
			{
				Provider:     providers.ProviderOpenAIGateway,
				Plaintext:    []byte("pat"),
				ModelAliases: map[string]string{"gpt-6-astra": "gpt-6-astra", "gpt-5": "gpt-5"},
			},
			{
				Provider:     providers.ProviderAnthropicGateway,
				Plaintext:    []byte("pat"),
				ModelAliases: map[string]string{"claude-haiku-4-5": "claude-haiku-4-5", "claude-opus-5": "claude-opus-5"},
			},
		})
		failed := router.Decision{
			Provider: providers.ProviderOpenAIGateway,
			Model:    "gpt-6-astra",
			Metadata: &router.RoutingMetadata{
				CandidateModels: []string{"claude-haiku-4-5", "claude-opus-5", "gpt-6-astra", "gpt-5"},
				RescueModels:    []string{"gpt-6-astra", "claude-opus-5", "gpt-5"},
			},
		}
		got := s.siblingFailoverDecisions(gwCtx, failed, 1_000, 0, 0)
		assert.Equal(t, []string{"claude-opus-5", "claude-haiku-4-5", "gpt-5"}, siblingModels(got))
		assert.Equal(t, providers.ProviderAnthropicGateway, got[0].Provider)
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
		_, ok := firstSibling(s, gwCtx, failed, 1_000, 0, 0)
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
		got, ok := firstSibling(s, gwCtx, failed, 1_000, 0, 0)
		require.True(t, ok)
		assert.Equal(t, "gpt-5", got.Model)
		assert.Equal(t, providers.ProviderOpenAIGateway, got.Provider)
	})

	t.Run("no metadata and legacy unkeyed deploys have no candidate", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic)
		_, ok := firstSibling(s, ctx, overloadedDecision(nil), 1_000, 0, 0)
		assert.False(t, ok)

		legacy := &Service{}
		_, ok = firstSibling(legacy, ctx, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"claude-sonnet-5"},
		}), 1_000, 0, 0)
		assert.False(t, ok, "an unset keyed-provider set can't prove a candidate is dispatchable")
	})
}

func TestSiblingFailover_ClusterScorerExhaustsModelTierBeforeAscending(t *testing.T) {
	s := siblingService(providers.ProviderAnthropic, providers.ProviderFireworks, providers.ProviderOpenAI)
	failed := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-haiku-4-5",
		Metadata: &router.RoutingMetadata{
			ClusterRouterVersion: "v-test",
			CandidateModels: []string{
				"claude-haiku-4-5", "gpt-4.1-mini", "gpt-4.1-nano",
				"claude-sonnet-5", "gpt-5",
			},
			CandidateScores: map[string]float32{
				"claude-haiku-4-5": 0.95, "gpt-4.1-mini": 0.8,
				"gpt-4.1-nano": 0.9, "claude-sonnet-5": 0.99, "gpt-5": 0.98,
			},
			CandidateProviders: map[string]string{
				"gpt-4.1-mini":    providers.ProviderOpenAI,
				"gpt-4.1-nano":    providers.ProviderOpenAI,
				"claude-sonnet-5": providers.ProviderAnthropic,
				"gpt-5":           providers.ProviderOpenAI,
			},
		},
	}

	got := s.siblingFailoverDecisions(context.Background(), failed, 1_000, 0, 0)
	assert.Equal(t, []string{"gpt-4.1-nano", "gpt-4.1-mini", "claude-sonnet-5", "gpt-5"}, siblingModels(got))
	assert.Empty(t, failed.Metadata.RescueModels, "rescue must not mutate the scorer's decision")

	failed.Model = "claude-sonnet-5"
	got = s.siblingFailoverDecisions(context.Background(), failed, 1_000, 0, 0)
	assert.Equal(t, []string{"gpt-5"}, siblingModels(got), "a mid-tier failure cannot fall down to low")
}

func TestSiblingFailover_RosterOrderBeatsProviderPreferenceAndExcludesUnlistedModels(t *testing.T) {
	s := siblingService(providers.ProviderAnthropic, providers.ProviderFireworks)
	failed := overloadedDecision(&router.RoutingMetadata{
		PolicyGroup:    "low",
		RosterFailover: true,
		RescueModels:   []string{"claude-opus-5", "claude-sonnet-5", "deepseek/deepseek-v4-pro"},
		CandidateModels: []string{
			"claude-opus-5", "deepseek/deepseek-v4-pro", "claude-sonnet-5", "claude-haiku-4-5",
		},
		CandidateProviders: map[string]string{
			"claude-sonnet-5":          providers.ProviderAnthropic,
			"deepseek/deepseek-v4-pro": providers.ProviderFireworks,
			"claude-haiku-4-5":         providers.ProviderAnthropic,
		},
	})

	got := s.siblingFailoverDecisions(context.Background(), failed, 1_000, 0, 0)
	assert.Equal(t, []string{"claude-sonnet-5", "deepseek/deepseek-v4-pro"}, siblingModels(got))
}
