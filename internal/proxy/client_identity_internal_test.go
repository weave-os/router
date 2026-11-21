package proxy

import (
	"testing"
	"weave-os/router/internal/requestcontext"

	"github.com/stretchr/testify/assert"
)

func TestCompletionLogClientAppBoundsCallerValues(t *testing.T) {
	assert.Equal(t, requestcontext.ClientAppCodex, completionLogClientApp(ClientIdentity{ClientApp: requestcontext.ClientAppCodex}))
	assert.Equal(t, requestcontext.ClientAppClaudeCode, completionLogClientApp(ClientIdentity{ClientApp: requestcontext.ClientAppClaudeCode}))
	assert.Equal(t, "other", completionLogClientApp(ClientIdentity{ClientApp: "my-custom-harness"}))
	assert.Equal(t, "eval", completionLogClientApp(ClientIdentity{ClientApp: requestcontext.ClientAppCodex, Eval: true}))
	assert.Empty(t, completionLogClientApp(ClientIdentity{}))
}
