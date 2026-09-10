package proxy

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func betaOptIn(ctx context.Context, key flags.Key, on bool) context.Context {
	return flags.WithOverrides(ctx, flags.Overrides{Bools: map[flags.Key]bool{key: on}})
}

func TestBetaProvidersDisabledForRequest(t *testing.T) {
	t.Run("no organization overrides disables every beta provider", func(t *testing.T) {
		got := betaProvidersDisabledForRequest(context.Background())
		for provider := range providers.BetaProviderFlags {
			assert.Contains(t, got, provider)
		}
		assert.Len(t, got, len(providers.BetaProviderFlags))
	})

	t.Run("an explicit false override keeps the provider disabled", func(t *testing.T) {
		ctx := betaOptIn(context.Background(), flags.KeyBetaProviderDeepSeek, false)
		assert.Contains(t, betaProvidersDisabledForRequest(ctx), providers.ProviderDeepSeek)
	})

	t.Run("opting in removes only that provider", func(t *testing.T) {
		ctx := betaOptIn(context.Background(), flags.KeyBetaProviderDeepSeek, true)
		got := betaProvidersDisabledForRequest(ctx)
		assert.NotContains(t, got, providers.ProviderDeepSeek)
		assert.Len(t, got, len(providers.BetaProviderFlags)-1)
	})
}

func TestPolicyExcludedProviders_BetaGate(t *testing.T) {
	t.Run("beta providers join the installation exclusions", func(t *testing.T) {
		s := &Service{}
		ctx := context.WithValue(context.Background(), InstallationExcludedProvidersContextKey{}, []string{providers.ProviderOpenAI})
		got := s.policyExcludedProviders(ctx)
		assert.Contains(t, got, providers.ProviderOpenAI)
		assert.Contains(t, got, providers.ProviderDeepSeek)
	})

	t.Run("the operator override does not re-admit a beta provider", func(t *testing.T) {
		s := (&Service{}).WithExcludedProvidersOverride([]string{providers.ProviderAnthropic})
		got := s.policyExcludedProviders(context.Background())
		assert.Contains(t, got, providers.ProviderAnthropic)
		assert.Contains(t, got, providers.ProviderDeepSeek,
			"ROUTER_EXCLUDED_PROVIDERS replaces the installation list, not the beta gate")
	})

	t.Run("opting in lifts the beta exclusion and nothing else", func(t *testing.T) {
		s := &Service{}
		ctx := betaOptIn(
			context.WithValue(context.Background(), InstallationExcludedProvidersContextKey{}, []string{providers.ProviderOpenAI}),
			flags.KeyBetaProviderDeepSeek, true)
		got := s.policyExcludedProviders(ctx)
		assert.Contains(t, got, providers.ProviderOpenAI)
		assert.NotContains(t, got, providers.ProviderDeepSeek)
	})

	t.Run("session strike-outs still merge on top of the gate", func(t *testing.T) {
		s := &Service{}
		ctx := context.WithValue(context.Background(), SessionDisabledProvidersContextKey{}, []string{providers.ProviderTogether})
		got := s.excludedProvidersForRequest(ctx)
		assert.Contains(t, got, providers.ProviderTogether)
		assert.Contains(t, got, providers.ProviderDeepSeek)
	})
}

// A deployment key for a beta provider is not enough to route to it: the
// organization must opt in, so an unset flag hides the provider even from the
// eligibility set every scorer, pin, and fallback path reads.
func TestEnabledProvidersForRequest_BetaProviderRequiresOptIn(t *testing.T) {
	makeService := func() *Service {
		return &Service{
			clients: dispatch.NewClients(map[string]providers.Client{
				providers.ProviderAnthropic: nil,
				providers.ProviderDeepSeek:  nil,
			}),
			deploymentKeyedProviders: map[string]struct{}{
				providers.ProviderAnthropic: {},
				providers.ProviderDeepSeek:  {},
			},
			passthroughEligibleProviders: map[string]struct{}{},
		}
	}

	t.Run("hidden by default", func(t *testing.T) {
		got := makeService().enabledProvidersForRequest(context.Background(), providers.ProviderAnthropic, http.Header{})
		assert.Contains(t, got, providers.ProviderAnthropic)
		assert.NotContains(t, got, providers.ProviderDeepSeek)
	})

	t.Run("served once the organization opts in", func(t *testing.T) {
		ctx := betaOptIn(context.Background(), flags.KeyBetaProviderDeepSeek, true)
		got := makeService().enabledProvidersForRequest(ctx, providers.ProviderAnthropic, http.Header{})
		assert.Contains(t, got, providers.ProviderAnthropic)
		assert.Contains(t, got, providers.ProviderDeepSeek)
	})
}

// /force-model on a DeepSeek model resolves past the beta binding to a GA
// provider for an organization that has not opted in, and refuses loudly
// when the beta provider is the only one the deployment can serve.
func TestForcedModelBinding_BetaProvider(t *testing.T) {
	const model = "deepseek/deepseek-v4-pro-0813"

	t.Run("falls to a GA binding without opt-in", func(t *testing.T) {
		s := &Service{deploymentKeyedProviders: map[string]struct{}{
			providers.ProviderTogether: {},
			providers.ProviderDeepSeek: {},
		}}
		binding, reason := s.forcedModelBinding(context.Background(), model, providers.ProviderTogether)
		require.Empty(t, reason)
		assert.Equal(t, providers.ProviderTogether, binding)
	})

	t.Run("refuses when only the beta provider is keyed", func(t *testing.T) {
		s := &Service{deploymentKeyedProviders: map[string]struct{}{providers.ProviderDeepSeek: {}}}
		binding, reason := s.forcedModelBinding(context.Background(), model, providers.ProviderTogether)
		assert.Empty(t, binding)
		assert.Contains(t, reason, providers.ProviderDeepSeek)
	})

	t.Run("pins the beta provider once opted in", func(t *testing.T) {
		s := &Service{deploymentKeyedProviders: map[string]struct{}{
			providers.ProviderTogether: {},
			providers.ProviderDeepSeek: {},
		}}
		ctx := betaOptIn(
			context.WithValue(context.Background(), InstallationExcludedProvidersContextKey{}, []string{providers.ProviderTogether}),
			flags.KeyBetaProviderDeepSeek, true)
		binding, reason := s.forcedModelBinding(ctx, model, providers.ProviderTogether)
		require.Empty(t, reason)
		assert.Equal(t, providers.ProviderDeepSeek, binding)
	})
}
