package proxy

import (
	"testing"
	"weave-os/router/internal/requestcontext"

	"github.com/stretchr/testify/assert"
)

func TestCompletionLogClientAppBoundsCallerValues(t *testing.T) {
	assert.Equal(t, requestcontext.ClientAppCodex, requestcontext.CompletionLogClientApp(ClientIdentity{ClientApp: requestcontext.ClientAppCodex}))
	assert.Equal(t, requestcontext.ClientAppClaudeCode, requestcontext.CompletionLogClientApp(ClientIdentity{ClientApp: requestcontext.ClientAppClaudeCode}))
	assert.Equal(t, "other", requestcontext.CompletionLogClientApp(ClientIdentity{ClientApp: "my-custom-harness"}))
	assert.Equal(t, "eval", requestcontext.CompletionLogClientApp(ClientIdentity{ClientApp: requestcontext.ClientAppCodex, Eval: true}))
	assert.Empty(t, requestcontext.CompletionLogClientApp(ClientIdentity{}))
}
