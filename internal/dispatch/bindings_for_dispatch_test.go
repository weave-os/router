package dispatch_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

const (
	multiBindingModel = "deepseek/deepseek-v4-pro"
	imageCappedModel  = "xiaomi/mimo-v2.6-pro"
)

func providersOf(bindings []catalog.ProviderBinding) []string {
	out := make([]string, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, b.Provider)
	}
	return out
}

func keyed(names ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

func TestOrderedBindingsWithoutFailoverServesOnlyTheDecisionProvider(t *testing.T) {
	got := dispatch.OrderedBindings(dispatch.BindingRequest{
		Decision: router.Decision{Model: multiBindingModel, Provider: providers.ProviderFireworks},
	})
	assert.Equal(t, []string{providers.ProviderFireworks}, providersOf(got))
}

func TestOrderedBindingsWithoutFailoverRejectsImageCappedBindingUnlessForced(t *testing.T) {
	req := dispatch.BindingRequest{
		Decision:   router.Decision{Model: imageCappedModel, Provider: providers.ProviderDeepInfra},
		ImageCount: 17,
	}
	assert.Nil(t, dispatch.OrderedBindings(req))

	req.UserForced = true
	assert.Equal(t, []string{providers.ProviderDeepInfra}, providersOf(dispatch.OrderedBindings(req)))
}

func TestOrderedBindingsKeepsTheDecisionProviderFirst(t *testing.T) {
	got := dispatch.OrderedBindings(dispatch.BindingRequest{
		Decision: router.Decision{Model: multiBindingModel, Provider: providers.ProviderOpenRouter},
		Keyed:    keyed(providers.ProviderFireworks, providers.ProviderOpenRouter),
	})
	require.GreaterOrEqual(t, len(got), 2)
	assert.Equal(t, providers.ProviderOpenRouter, got[0].Provider)
	assert.NotContains(t, providersOf(got[1:]), providers.ProviderOpenRouter)
}

func TestOrderedBindingsSubscriptionAdmitsAnUnkeyedDecisionProvider(t *testing.T) {
	req := dispatch.BindingRequest{
		Decision: router.Decision{Model: multiBindingModel, Provider: providers.ProviderFireworks},
		Keyed:    keyed(providers.ProviderOpenRouter),
	}
	assert.Equal(t, []string{providers.ProviderFireworks}, providersOf(dispatch.OrderedBindings(req)),
		"one keyed fallback collapses to the decision provider alone")

	req.SubscriptionServes = true
	with := providersOf(dispatch.OrderedBindings(req))
	assert.Equal(t, []string{providers.ProviderFireworks, providers.ProviderOpenRouter}, with)
	assert.Equal(t, keyed(providers.ProviderOpenRouter), req.Keyed, "the keyed set must not be mutated")
}

func TestOrderedBindingsDropsImageCappedPrimary(t *testing.T) {
	got := dispatch.OrderedBindings(dispatch.BindingRequest{
		Decision:   router.Decision{Model: imageCappedModel, Provider: providers.ProviderDeepInfra},
		Keyed:      keyed(providers.ProviderDeepInfra, providers.ProviderOpenRouter),
		ImageCount: 17,
	})
	assert.Equal(t, []string{providers.ProviderOpenRouter}, providersOf(got))
}

func TestOrderedBindingsImageCapLeavingNothingFailsUnlessForced(t *testing.T) {
	req := dispatch.BindingRequest{
		Decision:   router.Decision{Model: imageCappedModel, Provider: providers.ProviderDeepInfra},
		Keyed:      keyed(providers.ProviderDeepInfra),
		ImageCount: 17,
	}
	assert.Nil(t, dispatch.OrderedBindings(req))

	req.UserForced = true
	assert.Equal(t, []string{providers.ProviderDeepInfra}, providersOf(dispatch.OrderedBindings(req)))
}

func TestOrderedBindingsUnboundForcedCatalogModelHasNoWalk(t *testing.T) {
	req := dispatch.BindingRequest{
		Decision: router.Decision{Model: multiBindingModel, Provider: providers.ProviderFireworks},
		Keyed:    keyed(providers.ProviderAnthropic),
	}
	assert.Equal(t, []string{providers.ProviderFireworks}, providersOf(dispatch.OrderedBindings(req)))

	req.ForceModel = true
	assert.Nil(t, dispatch.OrderedBindings(req))
}

func TestOrderedBindingsExcludedPrimaryWithNoBindingsHasNoWalk(t *testing.T) {
	got := dispatch.OrderedBindings(dispatch.BindingRequest{
		Decision: router.Decision{Model: multiBindingModel, Provider: providers.ProviderFireworks},
		Keyed:    keyed(providers.ProviderFireworks, providers.ProviderOpenRouter),
		Excluded: keyed(providers.ProviderFireworks, providers.ProviderOpenRouter),
	})
	assert.Nil(t, got)
}
