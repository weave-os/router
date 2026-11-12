package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCompletionLogClientAppBoundsCallerValues(t *testing.T) {
	assert.Equal(t, ClientAppCodex, completionLogClientApp(ClientIdentity{ClientApp: ClientAppCodex}))
	assert.Equal(t, ClientAppClaudeCode, completionLogClientApp(ClientIdentity{ClientApp: ClientAppClaudeCode}))
	assert.Equal(t, "other", completionLogClientApp(ClientIdentity{ClientApp: "my-custom-harness"}))
	assert.Equal(t, "eval", completionLogClientApp(ClientIdentity{ClientApp: ClientAppCodex, Eval: true}))
	assert.Equal(t, "", completionLogClientApp(ClientIdentity{}))
}
