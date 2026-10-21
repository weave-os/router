package catalog_test

import (
	"testing"

	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
)

func TestInputTokenScale(t *testing.T) {
	assert.InDelta(t, 1.8, catalog.InputTokenScale("gpt-6-luna", "claude-opus-5-5"), 1e-9)
	assert.InDelta(t, 1/1.8, catalog.InputTokenScale("claude-opus-5-5", "gpt-6-luna"), 1e-9)
	assert.Equal(t, 1.0, catalog.InputTokenScale("gpt-6-luna", "gpt-6.1-sol"), "same tokenizer")
	assert.Equal(t, 1.0, catalog.InputTokenScale("claude-opus-5-5", "claude-opus-5-5"))
	assert.Equal(t, 1.0, catalog.InputTokenScale("gpt-6-luna", "not-a-model"), "unknown requested model")
	assert.Equal(t, 1.0, catalog.InputTokenScale("z-ai/glm-5.3", "claude-opus-5-5"), "unmeasured pair")
}
