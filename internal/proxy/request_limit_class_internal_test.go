package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

const oversizedAnthropicBodyBytes = 40 << 20

func TestRequestBodyTooLargeForForcedProviderOverItsLimit(t *testing.T) {
	ctx := withRequestShape(t.Context(), 0, oversizedAnthropicBodyBytes)

	err := requestBodyTooLargeFor(ctx, router.Decision{Model: "claude-sonnet-4-6", Provider: providers.ProviderAnthropic})
	require.ErrorIs(t, err, policy.ErrRequestBodyTooLarge)
	class, classified := ClassifyDispatchError(err)
	require.True(t, classified)
	assert.Equal(t, DispatchErrorRequestBodyTooLarge, class.Kind)

	assert.NoError(t, requestBodyTooLargeFor(withRequestShape(t.Context(), 0, 1<<20),
		router.Decision{Model: "claude-sonnet-4-6", Provider: providers.ProviderAnthropic}))
}

func TestWithRequestBodyBytesKeepsExplicitSizeWithoutContextShape(t *testing.T) {
	req := withRequestBodyBytes(t.Context(), router.Request{RequestBodyBytes: oversizedAnthropicBodyBytes})
	assert.Equal(t, oversizedAnthropicBodyBytes, req.RequestBodyBytes)

	req = withRequestBodyBytes(withRequestShape(t.Context(), 0, 5<<20), router.Request{})
	assert.Equal(t, 5<<20, req.RequestBodyBytes)
}

func TestPrimaryBindingWithinBodyLimitDropsOversizedSubscriptionPrimary(t *testing.T) {
	decision := router.Decision{Model: "claude-sonnet-4-6", Provider: providers.ProviderAnthropic}

	assert.Empty(t, primaryBindingWithinBodyLimit(withRequestShape(t.Context(), 0, oversizedAnthropicBodyBytes), decision))
	assert.Equal(t, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}},
		primaryBindingWithinBodyLimit(withRequestShape(t.Context(), 0, 1<<20), decision))
}
