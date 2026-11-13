package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEmptyCandidateError_NamesTheGatewayCase(t *testing.T) {
	err := emptyCandidateError([]Diagnostic{
		{CatalogID: "claude-opus-5", Reason: ExclusionGatewayNotServed},
		{CatalogID: "gpt-5.5", Reason: ExclusionGatewayNotServed},
	})

	assert.ErrorIs(t, err, ErrGatewayServesNoDeployedModel)
	assert.ErrorIs(t, err, ErrNoRoutableModels)
}

func TestEmptyCandidateError_MixedReasonsStayGeneric(t *testing.T) {
	// One non-gateway drop means aliases are not the whole story, so pointing
	// the caller at them would send them to fix the wrong setting.
	err := emptyCandidateError([]Diagnostic{
		{CatalogID: "claude-opus-5", Reason: ExclusionGatewayNotServed},
		{CatalogID: "gpt-5.5", Reason: ExclusionRequested},
	})

	assert.ErrorIs(t, err, ErrNoRoutableModels)
	assert.NotErrorIs(t, err, ErrGatewayServesNoDeployedModel)
}

func TestEmptyCandidateError_NoDiagnostics(t *testing.T) {
	assert.ErrorIs(t, emptyCandidateError(nil), ErrNoRoutableModels)
}

func TestEmptyCandidateError_SizeIsNotConfiguration(t *testing.T) {
	// A pool emptied partly by size answers with the client's native
	// prompt-too-long, the only error a harness compacts on.
	err := emptyCandidateError([]Diagnostic{
		{CatalogID: "claude-opus-5", Reason: ExclusionContextWindow},
		{CatalogID: "gpt-5.5", Reason: ExclusionRequested},
	})
	assert.ErrorIs(t, err, ErrContextWindowExceeded)
	assert.NotErrorIs(t, err, ErrNoRoutableModels)

	// An overflow-admitted model that is unavailable for another reason is
	// not a context-window failure; compaction cannot fix its missing roster.
	err = emptyCandidateError([]Diagnostic{
		{CatalogID: "gpt-6-luna", Reason: ExclusionUnmappedRoster},
	})
	assert.ErrorIs(t, err, ErrNoRoutableModels)
	assert.NotErrorIs(t, err, ErrContextWindowExceeded)

	err = emptyCandidateError([]Diagnostic{
		{CatalogID: "gemini-3.1-pro-preview", Reason: ExclusionUnsignedHistory},
	})
	assert.ErrorIs(t, err, ErrNoRoutableModels)
	assert.NotErrorIs(t, err, ErrContextWindowExceeded)

	err = emptyCandidateError([]Diagnostic{
		{CatalogID: "gpt-6-luna", Reason: ExclusionGatewayNotServed},
	})
	assert.ErrorIs(t, err, ErrGatewayServesNoDeployedModel, "a gateway serving nothing is empty at any size")
}

func TestCandidateLogFields_GroupsExclusionsByReason(t *testing.T) {
	fields := candidateLogFields(ResolvedCandidates{
		Candidates: []Candidate{{CatalogID: "gpt-4.1-mini"}, {CatalogID: "gemini-2.5-flash"}},
		Diagnostics: []Diagnostic{
			{CatalogID: "claude-opus-5", Reason: ExclusionRequested},
			{CatalogID: "z-ai/glm-5", Reason: ExclusionContextWindow},
			{CatalogID: "gpt-5.6-terra", Reason: ExclusionRequested},
		},
	})

	assert.Equal(t, []any{
		"candidate_count", 2,
		"candidates", "gpt-4.1-mini,gemini-2.5-flash",
		"excluded_" + string(ExclusionContextWindow), "z-ai/glm-5",
		"excluded_" + string(ExclusionRequested), "claude-opus-5,gpt-5.6-terra",
	}, fields)
}

func TestEmptyCandidateError_NamesMissingProviderCredentials(t *testing.T) {
	// Unkeyed deployments drop every model for want of a provider; telling
	// the caller to widen a model selection would send them to the wrong setting.
	err := emptyCandidateError([]Diagnostic{
		{CatalogID: "claude-opus-5", Reason: ExclusionNoProvider},
		{CatalogID: "deepseek/deepseek-v4.1-flash", Reason: ExclusionProviderPolicy},
		{CatalogID: "gpt-6-luna", Reason: ExclusionUnmappedRoster},
	})
	assert.ErrorIs(t, err, ErrNoProviderCredentials)
	assert.ErrorIs(t, err, ErrNoRoutableModels)

	err = emptyCandidateError([]Diagnostic{
		{CatalogID: "claude-opus-5", Reason: ExclusionNoProvider},
		{CatalogID: "gpt-5.5", Reason: ExclusionRequested},
	})
	assert.ErrorIs(t, err, ErrNoRoutableModels)
	assert.NotErrorIs(t, err, ErrNoProviderCredentials, "an org exclusion is a model-selection cause")

	err = emptyCandidateError([]Diagnostic{
		{CatalogID: "gpt-6-luna", Reason: ExclusionUnmappedRoster},
	})
	assert.NotErrorIs(t, err, ErrNoProviderCredentials)
}
