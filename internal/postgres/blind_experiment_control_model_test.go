package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServableControlModelFallsBackForUnservableValues(t *testing.T) {
	ctx := context.Background()
	assert.Equal(t, "claude-opus-5-5", servableControlModel(ctx, "installation", "claude-opus-5-5"))
	for _, invalid := range []string{"", "weave-auto", "weave-auto-efficient", "claude-opus-typo", "claude-opus-5-5-20991231"} {
		assert.Empty(t, servableControlModel(ctx, "installation", invalid), invalid)
	}
}
