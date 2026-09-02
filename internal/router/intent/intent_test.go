package intent_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"workweave/router/internal/router/intent"
)

func TestDetectCombinesStructuralAndKeywordSignalsInStableOrder(t *testing.T) {
	got := intent.Detect(intent.Signals{
		PromptText:           "Please analyze and implement the API workflow",
		HasTools:             true,
		HasImages:            true,
		EstimatedInputTokens: 40_000,
	})

	assert.Equal(t, []string{
		intent.TagAgentic,
		intent.TagCoding,
		intent.TagDeepReasoning,
		intent.TagLongContext,
		intent.TagVision,
	}, got)
}

func TestDetectRequiresCompleteKeywordTokens(t *testing.T) {
	got := intent.Detect(intent.Signals{PromptText: "testing this presentation"})
	assert.Empty(t, got, "partial keyword matches must not invent a coding intent")
}

func TestDetectMarksSummarizationWithoutReadingRequestBody(t *testing.T) {
	got := intent.Detect(intent.Signals{PromptText: "summarize this report"})
	assert.Equal(t, []string{intent.TagSummarization}, got)
}
