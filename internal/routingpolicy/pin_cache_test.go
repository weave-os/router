package routingpolicy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
)

func TestBaseModelOfStripsEffort(t *testing.T) {
	gpt := string(catalog.ModelIDGPT55)
	assert.Equal(t, gpt, candidates.BaseModelOf(gpt+":high"))
	assert.Equal(t, string(catalog.ModelIDClaudeHaiku45), candidates.BaseModelOf(string(catalog.ModelIDClaudeHaiku45)))
}
