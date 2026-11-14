package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router/catalog"
)

func TestHigherTierCandidates_TopTierFirst(t *testing.T) {
	got := higherTierCandidates(catalog.TierLow,
		[]string{"claude-haiku-4-5", "claude-sonnet-4-6", "claude-opus-4-6", "claude-opus-4-7"},
		map[string]float32{"claude-opus-4-6": 0.9, "claude-opus-4-7": 0.4, "claude-sonnet-4-6": 0.99})
	assert.Equal(t, []string{"claude-opus-4-6", "claude-opus-4-7", "claude-sonnet-4-6"}, got)
	assert.Empty(t, higherTierCandidates(catalog.TierHigh, []string{"claude-haiku-4-5", "claude-opus-4-7"}, nil))
}

func TestLowerTierCandidates(t *testing.T) {
	assert.Equal(t, []string{"claude-haiku-4-5", "claude-sonnet-4-6"},
		lowerTierCandidates("claude-opus-4-7", []string{"claude-haiku-4-5", "claude-sonnet-4-6", "claude-opus-4-6", "claude-opus-4-7"}))
}
