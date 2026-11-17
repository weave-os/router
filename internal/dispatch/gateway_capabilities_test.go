package dispatch_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/dispatch"
)

func TestGatewayCapabilitiesRemembersPerEndpointAndKind(t *testing.T) {
	var caps dispatch.GatewayCapabilities
	caps.RememberLacksResponses("https://gw-a.example")
	caps.RememberRejectsPromptCacheKey("https://gw-b.example")

	assert.True(t, caps.LacksResponses("https://gw-a.example"))
	assert.False(t, caps.LacksResponses("https://gw-b.example"))
	assert.True(t, caps.RejectsPromptCacheKey("https://gw-b.example"))
	assert.False(t, caps.RejectsPromptCacheKey("https://gw-a.example"))
}

func TestGatewayCapabilitiesNeverMemoizesTheEmptyKey(t *testing.T) {
	var caps dispatch.GatewayCapabilities
	caps.RememberLacksResponses("")
	caps.RememberRejectsPromptCacheKey("")

	assert.False(t, caps.LacksResponses(""), "direct vendors are not memoized")
	assert.False(t, caps.RejectsPromptCacheKey(""), "direct vendors are not memoized")
}
