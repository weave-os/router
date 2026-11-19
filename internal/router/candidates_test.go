package router_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
)

func TestCandidatesAdmitsOnlyModelsEveryStrategyTreatsAsEligible(t *testing.T) {
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

	for model, admitted := range map[string]bool{
		"allowed":         true,
		"excluded":        false,
		"context":         false,
		"unsigned":        false,
		"not-allowlisted": false,
		"safety":          true,
		"image":           true,
		"automatic":       true,
	} {
		assert.Equal(t, admitted, candidates.PassesHardFilters(model), model)
	}
	assert.True(t, router.Candidates{}.PassesHardFilters("any-model"), "an empty candidate set constrains nothing")
}
