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
