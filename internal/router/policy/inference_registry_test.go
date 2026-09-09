package policy_test

import (
	"strings"
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
	index := policyIndex(t, specs, policy.PurposeAnthropicMessages)

	duplicate := specs[index]
	duplicate.PolicyID = "duplicate-purpose-policy"
	_, err := policy.NewRegistry(append(specs, duplicate))
	require.Error(t, err)
	assert.ErrorContains(t, err, "more than one policy")

	_, err = policy.NewRegistry(append(specs[:index:index], specs[index+1:]...))
	require.Error(t, err)
	assert.ErrorContains(t, err, "has no policy")
}

func TestInferenceRegistryRejectsMissingRequiredFields(t *testing.T) {
	specs := policy.DefaultRegistry().Specs()
	index := policyIndex(t, specs, policy.PurposeAnthropicMessages)
	specs[index].Rationale = ""

	_, err := policy.NewRegistry(specs)
	require.Error(t, err)
	assert.ErrorContains(t, err, "missing policy identity, owner, rationale, or revision")

	specs = policy.DefaultRegistry().Specs()
	specs[index].PolicyID = "  "
	_, err = policy.NewRegistry(specs)
	require.Error(t, err)
	assert.ErrorContains(t, err, "missing policy identity, owner, rationale, or revision")

	specs = policy.DefaultRegistry().Specs()
	specs[index].PolicyRevision = "\t"
	_, err = policy.NewRegistry(specs)
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
	fixedIndex := policyIndex(t, specs, policy.PurposeHandoverSummary)
	specs[fixedIndex].Fallback = policy.FallbackSpec{
		Kind:         policy.FallbackKindPlanAlternatives,
		Alternatives: []string{"claude-haiku-4-5"},
	}
	_, err = policy.NewRegistry(specs)
	require.Error(t, err)
	assert.ErrorContains(t, err, "repeats fixed catalog model")

	specs = policy.DefaultRegistry().Specs()
	specs[index].OverridePrecedence = []policy.OverrideSource{policy.OverrideSourceRequest, policy.OverrideSourceRequest}
	_, err = policy.NewRegistry(specs)
	require.Error(t, err)
	assert.ErrorContains(t, err, "repeats override source")

	specs = policy.DefaultRegistry().Specs()
	specs[index].OverridePrecedence = []policy.OverrideSource{
		policy.OverrideSourceDeployment,
		policy.OverrideSourceRequest,
	}
	_, err = policy.NewRegistry(specs)
	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid override precedence order")
}

func TestInferenceRegistryRejectsInvalidPreferencesAndBudgets(t *testing.T) {
	specs := policy.DefaultRegistry().Specs()
	index := policyIndex(t, specs, policy.PurposeAnthropicMessages)
	specs[index].SoftPreferences = append(specs[index].SoftPreferences, policy.SoftPreference("unknown"))
	_, err := policy.NewRegistry(specs)
	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid soft preference")

	specs = policy.DefaultRegistry().Specs()
	specs[index].Budget.MaxSpendUSD = -1
	_, err = policy.NewRegistry(specs)
	require.Error(t, err)
	assert.ErrorContains(t, err, "negative budget")
}

func TestInferenceRegistryRejectsSelectionCandidateMismatch(t *testing.T) {
	specs := policy.DefaultRegistry().Specs()
	index := policyIndex(t, specs, policy.PurposeAnthropicMessages)
	specs[index].CandidateSource = policy.CandidateSourceDeployment

	_, err := policy.NewRegistry(specs)
	require.Error(t, err)
	assert.ErrorContains(t, err, "must use routable catalog candidates")
}

func TestInferenceRegistryReturnsImmutableCopies(t *testing.T) {
	registry := policy.DefaultRegistry()
	specs := registry.Specs()
	index := policyIndex(t, specs, policy.PurposeAnthropicMessages)
	require.NotEmpty(t, specs[index].HardConstraints)
	require.NotEmpty(t, specs[index].SoftPreferences)
	specs[index].HardConstraints[0] = policy.Constraint("changed")
	specs[index].SoftPreferences[0] = policy.SoftPreference("changed")
	specs[index].Fallback.Alternatives = append(specs[index].Fallback.Alternatives, "changed")

	unchanged, found := registry.Spec(policy.PurposeAnthropicMessages)
	require.True(t, found)
	assert.NotEqual(t, policy.Constraint("changed"), unchanged.HardConstraints[0])
	assert.NotEqual(t, policy.SoftPreference("changed"), unchanged.SoftPreferences[0])
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

func TestInferenceRegistryMarkdownKeepsCellsIntact(t *testing.T) {
	specs := policy.DefaultRegistry().Specs()
	index := policyIndex(t, specs, policy.PurposeHandoverSummary)
	specs[index].Owner = "@owner|team"
	specs[index].Rationale = "first line\r\nsecond | line"
	registry, err := policy.NewRegistry(specs)
	require.NoError(t, err)

	markdown := string(registry.StaticMarkdown())
	assert.Contains(t, markdown, "`@owner\\|team`")
	assert.Contains(t, markdown, "first line second \\| line")
	assert.NotContains(t, markdown, "\r")
	columns := 0
	for _, line := range strings.Split(strings.TrimSpace(markdown), "\n") {
		if strings.HasPrefix(line, "| Purpose |") {
			columns = strings.Count(line, "|")
		}
		if strings.HasPrefix(line, "| `") {
			assert.Equal(t, columns, strings.Count(strings.ReplaceAll(line, "\\|", ""), "|"), line)
		}
	}
	require.Positive(t, columns)
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
