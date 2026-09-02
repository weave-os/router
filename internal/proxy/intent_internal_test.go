package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"workweave/router/internal/router"
	"workweave/router/internal/router/intent"
)

func TestWithPolicyRequestContextDerivesIntentTags(t *testing.T) {
	req := (&Service{}).withPolicyRequestContext(context.Background(), router.Request{
		PromptText:           "debug this function",
		HasTools:             true,
		EstimatedInputTokens: 40_000,
	})

	assert.Equal(t, []string{
		intent.TagAgentic,
		intent.TagCoding,
		intent.TagLongContext,
	}, req.IntentTags)
}

func TestWithPolicyRequestContextPreservesExplicitIntentTags(t *testing.T) {
	req := (&Service{}).withPolicyRequestContext(context.Background(), router.Request{
		PromptText: "debug this function",
		IntentTags: []string{"operator_defined"},
	})

	assert.Equal(t, []string{"operator_defined"}, req.IntentTags)
}
