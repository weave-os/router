package policy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/eligibility"
	"weave-os/router/internal/router/policy"
)

func maxResolver() *policy.Resolver {
	return policy.NewResolver(
		set("deepseek/deepseek-v4-pro", "claude-opus-4-8", "muse-spark-1.3"),
		set(providers.ProviderFireworks, providers.ProviderAnthropic, providers.ProviderMeta),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)
}

func TestResolverWithoutProductBoundaryKeepsEveryModel(t *testing.T) {
	resolved := maxResolver().Resolve(router.Request{})

	assert.ElementsMatch(t,
		[]string{"deepseek/deepseek-v4-pro", "claude-opus-4-8", "muse-spark-1.3"},
		catalogIDs(resolved.Candidates),
	)
}

func TestResolverDropsClosedAndUnknownSourceModelsUnderMax(t *testing.T) {
	resolved := maxResolver().Resolve(router.Request{ProductEligibility: eligibility.MaxOpenSourceOnly})

	require.Equal(t, []string{"deepseek/deepseek-v4-pro"}, catalogIDs(resolved.Candidates))
	for _, id := range []string{"claude-opus-4-8", "muse-spark-1.3"} {
		assert.Containsf(t, resolved.Diagnostics, policy.Diagnostic{
			CatalogID: id,
			Reason:    policy.ExclusionProductIneligible,
		}, "%q should be excluded as product-ineligible", id)
	}
}

// The boundary is not a preference the resolver may drop to keep a pool
// non-empty, and not something an org allowlist naming the model re-opens.
func TestMaxBoundaryIsHardEvenWhenItEmptiesThePool(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-4-8"),
		set(providers.ProviderAnthropic),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		ProductEligibility: eligibility.MaxOpenSourceOnly,
		AllowedModels:      set("claude-opus-4-8"),
		PreferredModels:    []string{"claude-opus-4-8"},
	})

	assert.Empty(t, resolved.Candidates)
}

// The boundary is also desugared into the request's hard exclusions, so both
// filters would drop the model. The product reason has to win, or operating
// the boundary means reading a diagnostic that blames the org's own config.
func TestProductRefusalOutranksTheDesugaredExclusion(t *testing.T) {
	resolved := maxResolver().Resolve(router.Request{
		ProductEligibility: eligibility.MaxOpenSourceOnly,
		ExcludedModels:     set("claude-opus-4-8", "muse-spark-1.3"),
	})

	require.Equal(t, []string{"deepseek/deepseek-v4-pro"}, catalogIDs(resolved.Candidates))
	for _, id := range []string{"claude-opus-4-8", "muse-spark-1.3"} {
		assert.Containsf(t, resolved.Diagnostics, policy.Diagnostic{
			CatalogID: id,
			Reason:    policy.ExclusionProductIneligible,
		}, "%q should report the product boundary, not the exclusion it was desugared into", id)
	}
}

// An eligible model the org excluded is still an ordinary request exclusion.
func TestEligibleModelStillReportsRequestedExclusion(t *testing.T) {
	resolved := maxResolver().Resolve(router.Request{
		ProductEligibility: eligibility.MaxOpenSourceOnly,
		ExcludedModels:     set("deepseek/deepseek-v4-pro"),
	})

	assert.Empty(t, resolved.Candidates)
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "deepseek/deepseek-v4-pro",
		Reason:    policy.ExclusionRequested,
	})
}

func catalogIDs(candidates []policy.Candidate) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.CatalogID)
	}
	return ids
}
