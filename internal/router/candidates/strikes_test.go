package candidates_test

import (
	"testing"

	"weave-os/router/internal/router/candidates"

	"github.com/stretchr/testify/assert"
)

func TestSessionDisabledProvidersKeepsTheForcedProviderEnabled(t *testing.T) {
	disabled := candidates.SessionDisabledProviders([]string{"anthropic", "openai"}, []string{"google"}, "openai")

	assert.ElementsMatch(t, []string{"anthropic", "google"}, disabled)
}

func TestWithoutProvidersLeavesTheCallersSetUntouched(t *testing.T) {
	enabled := map[string]struct{}{"anthropic": {}, "openai": {}}

	kept := candidates.WithoutProviders(enabled, []string{"openai"})

	assert.Equal(t, map[string]struct{}{"anthropic": {}}, kept)
	assert.Len(t, enabled, 2)
	assert.Nil(t, candidates.WithoutProviders(nil, []string{"openai"}))
}
