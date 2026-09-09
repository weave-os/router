package policy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/policy"
)

func TestDefaultInferenceRegistryCoversEveryPurpose(t *testing.T) {
	registry := policy.DefaultRegistry()
	require.NotEmpty(t, registry.Revision())
	assert.Len(t, registry.Specs(), len(policy.KnownPurposes()))
	for _, purpose := range policy.KnownPurposes() {
		spec, found := registry.Spec(purpose)
		require.Truef(t, found, "purpose %q must be registered", purpose)
		assert.Equal(t, purpose, spec.Purpose)
		assert.NotEmpty(t, spec.PolicyID)
		assert.NotEmpty(t, spec.Rationale)
		assert.NotEmpty(t, spec.Owner)
	}
}

func TestInferenceRegistryRejectsDuplicateAndMissingPurposes(t *testing.T) {
	specs := policy.DefaultRegistry().Specs()

	duplicate := specs[0]
	duplicate.PolicyID = "duplicate-purpose-policy"
	_, err := policy.NewRegistry(append(specs, duplicate))
	require.Error(t, err)
	assert.ErrorContains(t, err, "more than one policy")

	_, err = policy.NewRegistry(specs[1:])
	require.Error(t, err)
	assert.ErrorContains(t, err, "has no policy")
}

func TestInferenceRegistryRejectsMissingRequiredFields(t *testing.T) {
	specs := policy.DefaultRegistry().Specs()
	specs[0].Rationale = ""

	_, err := policy.NewRegistry(specs)
	require.Error(t, err)
	assert.ErrorContains(t, err, "missing policy identity, owner, rationale, or revision")
}

func TestInferenceRegistryRejectsInvalidFixedPolicy(t *testing.T) {
	specs := policy.DefaultRegistry().Specs()
	index := policyIndex(t, specs, policy.PurposeHandoverSummary)

	specs[index].FixedCatalogModels = nil
	_, err := policy.NewRegistry(specs)
	require.Error(t, err)
	assert.ErrorContains(t, err, "has no catalog models")

	specs = policy.DefaultRegistry().Specs()
	specs[index].FixedCatalogModels = []string{"not-a-catalog-model"}
	_, err = policy.NewRegistry(specs)
	require.Error(t, err)
	assert.ErrorContains(t, err, "unknown catalog model")

	specs = policy.DefaultRegistry().Specs()
	specs[index].CandidateSource = policy.CandidateSourceDeployment
	_, err = policy.NewRegistry(specs)
	require.Error(t, err)
	assert.ErrorContains(t, err, "must use the fixed catalog candidate source")

	specs = policy.DefaultRegistry().Specs()
	mainIndex := policyIndex(t, specs, policy.PurposeAnthropicMessages)
	specs[mainIndex].FixedCatalogModels = []string{"claude-haiku-4-5"}
	_, err = policy.NewRegistry(specs)
	require.Error(t, err)
	assert.ErrorContains(t, err, "declares fixed catalog models")
}

func TestInferenceRegistryRejectsInvalidFallbackAndOverridePrecedence(t *testing.T) {
	specs := policy.DefaultRegistry().Specs()
	index := policyIndex(t, specs, policy.PurposeAnthropicMessages)

	specs[index].Fallback.Kind = policy.FallbackKind("arbitrary")
	_, err := policy.NewRegistry(specs)
	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid typed value")

	specs = policy.DefaultRegistry().Specs()
	specs[index].Fallback = policy.FallbackSpec{Kind: policy.FallbackKindPlanAlternatives}
	_, err = policy.NewRegistry(specs)
	require.Error(t, err)
	assert.ErrorContains(t, err, "without alternatives")

	specs = policy.DefaultRegistry().Specs()
	specs[index].Fallback.Alternatives = []string{"claude-haiku-4-5"}
	_, err = policy.NewRegistry(specs)
	require.Error(t, err)
	assert.ErrorContains(t, err, "declares alternatives for fallback kind")

	specs = policy.DefaultRegistry().Specs()
	specs[index].OverridePrecedence = []policy.OverrideSource{policy.OverrideSourceRequest, policy.OverrideSourceRequest}
	_, err = policy.NewRegistry(specs)
	require.Error(t, err)
	assert.ErrorContains(t, err, "repeats override source")
}

func TestInferenceRegistryReturnsImmutableCopies(t *testing.T) {
	registry := policy.DefaultRegistry()
	specs := registry.Specs()
	specs[0].HardConstraints[0] = policy.Constraint("changed")
	specs[0].Fallback.Alternatives = append(specs[0].Fallback.Alternatives, "changed")

	unchanged, found := registry.Spec(specs[0].Purpose)
	require.True(t, found)
	assert.NotEqual(t, policy.Constraint("changed"), unchanged.HardConstraints[0])
	assert.NotContains(t, unchanged.Fallback.Alternatives, "changed")
}

func TestInferenceRegistryProjectionIsDeterministic(t *testing.T) {
	registry := policy.DefaultRegistry()
	firstJSON, err := registry.StaticJSON()
	require.NoError(t, err)
	secondJSON, err := registry.StaticJSON()
	require.NoError(t, err)
	firstMarkdown := registry.StaticMarkdown()
	secondMarkdown := registry.StaticMarkdown()

	assert.Equal(t, firstJSON, secondJSON)
	assert.Equal(t, firstMarkdown, secondMarkdown)
	assert.Contains(t, string(firstMarkdown), registry.Revision())
}

func policyIndex(t *testing.T, specs []policy.PolicySpec, purpose policy.Purpose) int {
	t.Helper()
	for index, spec := range specs {
		if spec.Purpose == purpose {
			return index
		}
	}
	require.FailNowf(t, "policy not found", "purpose %q", purpose)
	return -1
}
