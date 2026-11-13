package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
)

func TestSubscriptionProvidersHonorRoutingToggle(t *testing.T) {
	ctx := context.WithValue(context.Background(), AnthropicSubscriptionContextKey{}, "sk-ant-oat01-token")
	require.Contains(t, subscriptionProvidersForRequest(ctx), providers.ProviderAnthropic)

	disabled := context.WithValue(ctx, InstallationSubscriptionRoutingDisabledContextKey{}, true)
	require.Empty(t, subscriptionProvidersForRequest(disabled))
	require.Empty(t, subscriptionProvidersForRequest(withSuppressedClaudeSubscription(ctx)))
}
