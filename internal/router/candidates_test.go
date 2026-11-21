package router_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
)

func TestCandidatesHardFilterViolationNamesEveryHardExclusionSet(t *testing.T) {
	set := func(models ...string) map[string]struct{} {
		out := make(map[string]struct{}, len(models))
		for _, model := range models {
			out[model] = struct{}{}
		}
		return out
	}
	candidates := router.Candidates{
		ExcludedModels:                set("excluded"),
		AllowedModels:                 set("allowed", "excluded", "context", "unsigned", "safety", "image", "automatic"),
		ContextWindowExcludedModels:   set("context"),
		UnsignedHistoryExcludedModels: set("unsigned"),
		SafetyExcludedModels:          set("safety"),
		ImageLimitExcludedModels:      set("image"),
		AutomaticExcludedModels:       set("automatic"),
	}

	for model, want := range map[string]string{
		"allowed":         "",
		"excluded":        "excluded_models",
		"context":         "context_window_excluded_models",
		"unsigned":        "unsigned_history_excluded_models",
		"not-allowlisted": "allowed_models",
		"safety":          "safety_excluded_models",
		"image":           "image_limit_excluded_models",
		"automatic":       "",
	} {
		violated, hasViolation := candidates.HardFilterViolation(model)
		assert.Equal(t, router.CandidateViolation(want), violated, model)
		assert.Equal(t, want != "", hasViolation, model)
	}
	assert.True(t, candidates.AutomaticallyExcludes("automatic"))
	assert.False(t, candidates.AutomaticallyExcludes("safety"))
	_, hasViolation := router.Candidates{}.HardFilterViolation("any-model")
	assert.False(t, hasViolation, "an empty candidate set constrains nothing")
}
