package main

import (
	"testing"

	"weave-os/router/internal/ingress"

	"github.com/stretchr/testify/assert"
)

func TestIngressFormatsForKeepsDecayDefaultForNonPositiveTurns(t *testing.T) {
	assert.Equal(t, ingress.DefaultSearchRequirementDecayTurns, ingressFormatsFor(false, true, 0, nil).SearchRequirementDecayTurns)
	assert.Equal(t, ingress.DefaultSearchRequirementDecayTurns, ingressFormatsFor(false, true, -2, nil).SearchRequirementDecayTurns)
	assert.Equal(t, 5, ingressFormatsFor(false, true, 5, nil).SearchRequirementDecayTurns)
}

func TestIngressFormatsForCarriesSearchAndHeaderSettings(t *testing.T) {
	formats := ingressFormatsFor(true, true, 3, nil)
	assert.True(t, formats.AllowedModelsHeader)
	assert.True(t, formats.ScopedSearchRequirement)
	assert.True(t, formats.OpenAIResponsesBroad)
	assert.True(t, formats.CCOrchToolsCrossVendor)
}
