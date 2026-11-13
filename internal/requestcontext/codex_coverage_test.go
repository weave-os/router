package requestcontext_test

import (
	"testing"

	"weave-os/router/internal/requestcontext"

	"github.com/stretchr/testify/assert"
)

func TestCodexSubscriptionAttemptEligibilityIncludesCatalogOpenAIModels(t *testing.T) {
	assert.True(t, requestcontext.CodexSubscriptionCanAttemptModel("gpt-6-astra"),
		"subscription funding should be attempted for a selected OpenAI catalog model outside the Codex automatic roster")
	for _, model := range []string{"gpt-future-model", "claude-opus-5", "openai/gpt-6-astra", "gpt-6-astra:high"} {
		assert.False(t, requestcontext.CodexSubscriptionCanAttemptModel(model),
			"unknown, non-OpenAI, and non-canonical targets must not be sent to Codex OAuth")
	}
}

func TestCodexLunaSubscriptionCoverage(t *testing.T) {
	const lunaModel = "gpt-6-luna"
	assert.True(t, requestcontext.CodexSubscriptionCoversModel(lunaModel))
	assert.Contains(t, requestcontext.CodexCoveredModels(), lunaModel)
	for _, model := range []string{"gpt-5.4-nano", "gpt-future-model", "openai/" + lunaModel, lunaModel + ":high"} {
		assert.False(t, requestcontext.CodexSubscriptionCoversModel(model), "only covered canonical model IDs may receive OAuth")
	}
}
