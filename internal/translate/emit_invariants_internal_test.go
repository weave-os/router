package translate

import (
	"net/http"
	"testing"

	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStablePromptCacheKey_ToolsAloneAreACacheablePrefix(t *testing.T) {
	withTools := stablePromptCacheKey([]byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"a"}}]}`))
	assert.NotEmpty(t, withTools)
	assert.Empty(t, stablePromptCacheKey([]byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[]}`)),
		"an empty tools array is not a cacheable prefix")
}

func TestApplySessionAffinity_NoSessionKeyAddsNoHeader(t *testing.T) {
	for _, provider := range []string{providers.ProviderXAI, providers.ProviderDeepInfra, providers.ProviderFireworks} {
		headers := http.Header{}
		_, err := applySessionAffinity([]byte(`{"messages":[]}`), headers, EmitOptions{TargetProvider: provider})
		require.NoError(t, err)
		assert.Empty(t, headers, provider)
	}
}
